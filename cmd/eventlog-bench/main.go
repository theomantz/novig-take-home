package main

import (
	"database/sql"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const schemaDDL = `
CREATE TABLE IF NOT EXISTS event_log (
	seq INTEGER PRIMARY KEY,
	event_id TEXT UNIQUE NOT NULL,
	ts_ms INTEGER NOT NULL,
	event_type TEXT NOT NULL,
	market_id TEXT NOT NULL,
	payload_json TEXT NOT NULL
);
`

const insertSQL = `
INSERT INTO event_log(seq, event_id, ts_ms, event_type, market_id, payload_json)
VALUES (?, ?, ?, ?, ?, ?)
`

type variant struct {
	name string
	ddl  []string
}

type querySpec struct {
	name       string
	sql        string
	kind       string
	paramLabel string
}

type durationStats struct {
	avg time.Duration
	p50 time.Duration
	p95 time.Duration
}

func main() {
	volumesFlag := flag.String("rows", "10000,100000,250000", "comma-separated event_log row counts")
	rowIterations := flag.Int("row-iterations", 5000, "iterations for scalar queries")
	rangeIterations := flag.Int("range-iterations", 25, "iterations for range scan queries")
	tailWindow := flag.Int("tail-window", 1000, "row window for tail replay range scan")
	flag.Parse()

	if *rowIterations < 1 || *rangeIterations < 1 || *tailWindow < 1 {
		fail("row-iterations, range-iterations, and tail-window must be >= 1")
	}

	volumes, err := parseCSVInts(*volumesFlag)
	if err != nil {
		fail("parse rows: %v", err)
	}
	if len(volumes) == 0 {
		fail("at least one row count must be provided")
	}

	variants := []variant{
		{name: "baseline"},
		{
			name: "with_seq_desc_index",
			ddl:  []string{"CREATE INDEX IF NOT EXISTS idx_event_log_seq_desc ON event_log(seq DESC);"},
		},
	}

	queries := []querySpec{
		{name: "max_seq", sql: "SELECT MAX(seq) FROM event_log", kind: "scalar", paramLabel: "n/a"},
		{name: "latest_desc_limit_1", sql: "SELECT seq FROM event_log ORDER BY seq DESC LIMIT 1", kind: "scalar", paramLabel: "n/a"},
		{name: "asc_replay_from_head", sql: "SELECT seq, event_id, ts_ms, event_type, market_id, payload_json FROM event_log WHERE seq >= ? ORDER BY seq ASC", kind: "range", paramLabel: "from_seq=1"},
		{name: "asc_replay_from_tail", sql: "SELECT seq, event_id, ts_ms, event_type, market_id, payload_json FROM event_log WHERE seq >= ? ORDER BY seq ASC", kind: "range", paramLabel: "from_seq=tail"},
	}

	fmt.Printf("# event_log read benchmark\n")
	fmt.Printf("Generated: %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Printf("Scalar iterations/query: %d\n", *rowIterations)
	fmt.Printf("Range iterations/query: %d\n", *rangeIterations)
	fmt.Printf("Tail replay window: %d rows\n\n", *tailWindow)

	for _, rowCount := range volumes {
		if rowCount < 1 {
			fail("row count must be >= 1 (got %d)", rowCount)
		}

		fmt.Printf("## Rows: %d\n\n", rowCount)
		fmt.Println("| Variant | Query | Param | Avg | P50 | P95 |")
		fmt.Println("| --- | --- | --- | --- | --- | --- |")

		for _, v := range variants {
			db, cleanup, err := buildDB(rowCount, v)
			if err != nil {
				fail("build db (%s, rows=%d): %v", v.name, rowCount, err)
			}

			tailFromSeq := max(1, rowCount-*tailWindow+1)
			for _, q := range queries {
				iterations := *rowIterations
				args := []any{}
				paramLabel := q.paramLabel

				if q.kind == "range" {
					iterations = *rangeIterations
					if q.name == "asc_replay_from_tail" {
						args = []any{tailFromSeq}
						paramLabel = fmt.Sprintf("from_seq=%d", tailFromSeq)
					} else {
						args = []any{1}
					}
				}

				stats, err := benchmarkQuery(db, q.kind, q.sql, iterations, args...)
				if err != nil {
					cleanup()
					fail("benchmark query (%s/%s): %v", v.name, q.name, err)
				}

				fmt.Printf("| %s | %s | %s | %s | %s | %s |\n",
					v.name,
					q.name,
					paramLabel,
					formatDuration(stats.avg),
					formatDuration(stats.p50),
					formatDuration(stats.p95),
				)
			}

			fmt.Printf("\nQuery plan (%s):\n", v.name)
			for _, q := range queries {
				args := []any{}
				if q.kind == "range" {
					if q.name == "asc_replay_from_tail" {
						args = []any{tailFromSeq}
					} else {
						args = []any{1}
					}
				}

				details, err := explainQueryPlan(db, q.sql, args...)
				if err != nil {
					cleanup()
					fail("explain query plan (%s/%s): %v", v.name, q.name, err)
				}
				for _, detail := range details {
					fmt.Printf("- %s: %s\n", q.name, detail)
				}
			}
			fmt.Println()
			cleanup()
		}
	}
}

func buildDB(rowCount int, v variant) (*sql.DB, func(), error) {
	dsn := fmt.Sprintf("file:eventlog_bench_%s_%d?mode=memory&cache=shared", v.name, time.Now().UnixNano())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	cleanup := func() {
		_ = db.Close()
	}

	if _, err := db.Exec(schemaDDL); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("create schema: %w", err)
	}
	for _, ddl := range v.ddl {
		if _, err := db.Exec(ddl); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("apply ddl %q: %w", ddl, err)
		}
	}

	if err := seedEvents(db, rowCount); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("seed events: %w", err)
	}
	return db, cleanup, nil
}

func seedEvents(db *sql.DB, rowCount int) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}

	stmt, err := tx.Prepare(insertSQL)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer stmt.Close()

	baseMs := time.Now().UnixMilli()
	for i := 1; i <= rowCount; i++ {
		seq := i
		eventID := fmt.Sprintf("event-%d", i)
		tsMs := baseMs + int64(i)
		if _, err := stmt.Exec(seq, eventID, tsMs, "MARKET_UPDATED", "market-news-001", `{"status":"OPEN"}`); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("insert row %d: %w", i, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func benchmarkQuery(db *sql.DB, kind string, query string, iterations int, args ...any) (durationStats, error) {
	durations := make([]time.Duration, 0, iterations)

	for i := 0; i < iterations; i++ {
		start := time.Now()
		switch kind {
		case "scalar":
			var v int64
			if err := db.QueryRow(query, args...).Scan(&v); err != nil {
				return durationStats{}, fmt.Errorf("scan scalar: %w", err)
			}
		case "range":
			rows, err := db.Query(query, args...)
			if err != nil {
				return durationStats{}, fmt.Errorf("query range: %w", err)
			}

			var (
				seq       int64
				eventID   string
				tsMs      int64
				eventType string
				marketID  string
				payload   string
			)
			for rows.Next() {
				if err := rows.Scan(&seq, &eventID, &tsMs, &eventType, &marketID, &payload); err != nil {
					rows.Close()
					return durationStats{}, fmt.Errorf("scan range: %w", err)
				}
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return durationStats{}, fmt.Errorf("iterate range: %w", err)
			}
			if err := rows.Close(); err != nil {
				return durationStats{}, fmt.Errorf("close rows: %w", err)
			}
		default:
			return durationStats{}, fmt.Errorf("unknown query kind: %s", kind)
		}
		durations = append(durations, time.Since(start))
	}

	return computeStats(durations), nil
}

func explainQueryPlan(db *sql.DB, query string, args ...any) ([]string, error) {
	explainSQL := "EXPLAIN QUERY PLAN " + query
	rows, err := db.Query(explainSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	details := make([]string, 0)
	for rows.Next() {
		var id, parent, notused int
		var detail string
		if err := rows.Scan(&id, &parent, &notused, &detail); err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return details, nil
}

func computeStats(durations []time.Duration) durationStats {
	if len(durations) == 0 {
		return durationStats{}
	}

	sorted := make([]time.Duration, len(durations))
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for i := range sorted {
		sum += sorted[i]
	}

	p50Idx := percentileIndex(len(sorted), 0.50)
	p95Idx := percentileIndex(len(sorted), 0.95)

	return durationStats{
		avg: time.Duration(int64(sum) / int64(len(sorted))),
		p50: sorted[p50Idx],
		p95: sorted[p95Idx],
	}
}

func percentileIndex(length int, percentile float64) int {
	if length == 0 {
		return 0
	}
	idx := int(math.Ceil(percentile*float64(length))) - 1
	if idx < 0 {
		return 0
	}
	if idx >= length {
		return length - 1
	}
	return idx
}

func parseCSVInts(raw string) ([]int, error) {
	parts := strings.Split(raw, ",")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not an integer", part)
		}
		out = append(out, v)
	}
	return out, nil
}

func formatDuration(d time.Duration) string {
	if d >= time.Millisecond {
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	}
	return fmt.Sprintf("%.2fµs", float64(d)/float64(time.Microsecond))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func fail(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
