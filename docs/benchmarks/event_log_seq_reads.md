# event_log sequence read benchmark (Issue #4)

Date: 2026-03-07

## Goal

Evaluate whether core `event_log` should add an explicit `seq DESC` index to optimize reverse-sequence reads used by checkpoint/latest-event paths.

Targeted query patterns:

- `SELECT MAX(seq) FROM event_log`
- `SELECT seq FROM event_log ORDER BY seq DESC LIMIT 1`
- `SELECT ... FROM event_log WHERE seq >= ? ORDER BY seq ASC`

## Method

Used `go run ./cmd/eventlog-bench` to compare two schema variants at 10k/100k/250k rows:

- `baseline`: existing schema (`seq INTEGER PRIMARY KEY`)
- `with_seq_desc_index`: baseline + `CREATE INDEX idx_event_log_seq_desc ON event_log(seq DESC)`

Benchmark settings:

- scalar queries (`MAX`, `DESC LIMIT 1`): 5000 iterations/query
- range queries (`ASC replay`): 25 iterations/query
- tail replay window: 1000 rows

Full raw output: [`event_log_seq_reads_run_2026-03-07.txt`](./event_log_seq_reads_run_2026-03-07.txt)

## Key results

At 250k rows:

| Query | Baseline Avg | With DESC Index Avg | Delta |
| --- | --- | --- | --- |
| `MAX(seq)` | `4.11µs` | `4.37µs` | `+6.3%` |
| `ORDER BY seq DESC LIMIT 1` | `4.15µs` | `4.14µs` | `-0.2%` |
| `ASC replay from head` (`from_seq=1`) | `408.14ms` | `397.98ms` | `-2.5%` |
| `ASC replay from tail` (`from_seq=249001`) | `1.69ms` | `1.69ms` | `0.0%` |

Query-plan highlights:

- Baseline already uses the integer primary key (`rowid`) for ordered `seq` range reads.
- The explicit DESC index is chosen for scalar reverse/max queries, but measured latency is not better.
- Across row volumes, gains are inconsistent and within normal run-to-run variance, so there is no clear win.

## Decision

Keep the current schema (no additional `seq DESC` index).

Rationale:

- No measured read improvement on target queries at representative volumes.
- Extra index adds write and memory overhead without payoff.
- Existing `seq INTEGER PRIMARY KEY` path is already efficient for replay ordering and latest-sequence lookups.

## Re-run

```bash
go run ./cmd/eventlog-bench
```
