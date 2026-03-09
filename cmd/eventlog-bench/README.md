# eventlog-bench findings and operating guidance

This document captures the current benchmark-backed guidance for `event_log` read behavior and index decisions.

Sources:

- `docs/benchmarks/event_log_seq_reads_run_2026-03-07.txt`
- `cmd/eventlog-bench/main.go`

## What we benchmarked

Harness command:

```bash
go run ./cmd/eventlog-bench
```

Baseline schema under test:

```sql
CREATE TABLE event_log (
  seq INTEGER PRIMARY KEY,
  event_id TEXT UNIQUE NOT NULL,
  ts_ms INTEGER NOT NULL,
  event_type TEXT NOT NULL,
  market_id TEXT NOT NULL,
  payload_json TEXT NOT NULL
)
```

Variant schema under test:

```sql
CREATE INDEX idx_event_log_seq_desc ON event_log(seq DESC)
```

Row volumes tested: `10k`, `100k`, `250k`

Queries tested:

- `SELECT MAX(seq) FROM event_log`
- `SELECT seq FROM event_log ORDER BY seq DESC LIMIT 1`
- `SELECT ... FROM event_log WHERE seq >= ? ORDER BY seq ASC` (from head and from tail)

## Measured results (baseline)

| Rows | max_seq p95 | latest_desc_limit_1 p95 | asc_replay_from_head p95 | asc_replay_from_tail p95 |
| --- | --- | --- | --- | --- |
| 10k | 4.42us | 4.54us | 16.86ms | 1.83ms |
| 100k | 4.88us | 4.12us | 185.63ms | 1.84ms |
| 250k | 4.88us | 4.83us | 471.99ms | 2.01ms |

Additional findings:

- `seq INTEGER PRIMARY KEY` already serves ordered/range reads efficiently via rowid.
- The explicit `seq DESC` index changed query plans for reverse/max reads but did not produce consistent latency wins.
- Tail replay latency stayed effectively flat around ~2ms p95 across tested row counts.
- Full replay from head scales roughly linearly with total row count.

## Comfortable row-count envelope (current architecture)

Based on current measurements, this architecture supports **at least 250k rows comfortably** for the core read patterns used by replication:

- latest sequence lookups remain in single-digit microseconds p95.
- tail catch-up replay (last ~1000 rows) remains around 2ms p95.
- full replay from seq 1 remains below 500ms p95 at 250k rows.

Interpretation:

- Normal steady-state replication work (tail/appended events) is not meaningfully degraded at 250k rows.
- Recovery paths that read from the head remain acceptable at 250k, but cost grows with log size and should be tracked as volume increases.

## Recommended SLOs and SLA posture

These targets are grounded in the benchmark output and intended for this current single-core + in-memory SQLite architecture.

### SLOs (internal objectives)

- `max_seq` / `latest_desc_limit_1`:
  - target: p95 <= `10us`
  - objective: `99.9%` of samples
- tail replay (`from_seq=last_applied_seq+1`, ~1000-row window):
  - target: p95 <= `3ms`
  - objective: `99%` of samples
- head replay (`from_seq=1`) with row volume <= 250k:
  - target: p95 <= `500ms`
  - objective: `99%` of samples

### SLA guidance (what we can credibly promise today)

- Latency SLA can mirror the above SLO bounds **for datasets up to 250k rows** in a healthy single-instance deployment.
- Availability/durability SLA should be conservative because the current architecture is in-memory and single-node by default.
- For stronger contractual SLAs (high uptime/durability guarantees), add persistent storage and failover before committing.

## When to consider indexing

Do **not** add `seq DESC` index now; current benchmark evidence does not justify it.

Revisit indexing when one or more conditions become true:

- Row-count growth pushes head replay p95 above `1s` for expected production recovery scenarios.
- `max_seq` or latest-seq lookups regress above `10us` p95 under representative load.
- New query patterns are introduced (for example filters on `market_id`, `event_type`, or `ts_ms`) and their p95 exceeds service targets.
- Query plans stop using efficient primary-key access for replay paths.

Index selection guidance:

- Keep relying on `seq INTEGER PRIMARY KEY` for sequence-only scans/lookups.
- Add indexes for **new predicates**, not for existing sequence queries unless benchmarks show clear wins.
- Benchmark each candidate index against realistic row counts (`250k`, `500k`, `1M`) before merge.

## Re-benchmark playbook

Run:

```bash
go run ./cmd/eventlog-bench -rows 10000,100000,250000,500000,1000000
```

Then compare:

- p95 for latest-seq lookups
- p95 for tail replay and head replay
- query plans for each query/variant
- write-path impact if additional indexes are proposed
