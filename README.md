# Novig Take-Home: Core/Replica Market Risk Replication (Go)

This project implements a distributed **core/replica** state replication system for sportsbook market risk controls.
It models a volatility circuit breaker using **bet-flow signals** (not odds/pricing).

## What is implemented

- `core`: authoritative state owner
  - Maintains in-memory `MarketState`
  - Evaluates circuit-breaker rules every 1 second over a rolling 30-second window
  - Emits ordered replication events (`seq`, `event_id`)
  - Persists event log in **in-memory SQLite**
  - Pushes events via **SSE** (`/stream`)
  - Serves snapshot (`/snapshot`) and scenario/injection endpoints
- `replica`: follower
  - Consumes SSE from core (push-based, no polling loop for replication)
  - Ensures at-least-once consumption + idempotent dedupe by `event_id`
  - Enforces strict in-order apply by `seq`
  - Recovers from sequence gaps by fetching snapshot and replacing local state
  - Stores dedupe/checkpoint in its own **in-memory SQLite**
  - Serves read-only APIs from replicated in-memory state

## Real-World Use Case

During a live game, breaking news causes a sudden surge of bets on one market.
The core detects abnormal bet-flow conditions (bet velocity and liability acceleration), auto-suspends the market, and emits suspension events.
Multiple replicas converge quickly to the same suspended state, so downstream read clients consistently see that the market is suspended.
After cooldown and normalized flow, the core auto-reopens the market and replicas converge to OPEN again.

This demonstrates sportsbook risk controls under volatility without implementing odds logic.

## Domain Model

### MarketState

- `market_id`
- `status`: `OPEN | SUSPENDED`
- `bet_count_30s`
- `stake_sum_30s_cents`
- `liability_delta_30s_cents`
- `cooldown_until_unix_ms`
- `last_reason`: `BET_RATE_SPIKE | LIABILITY_SPIKE`
- `last_transition_unix_ms`

### ReplicationEvent

- `seq`
- `event_id`
- `ts_unix_ms`
- `event_type`: `MARKET_UPDATED | MARKET_SUSPENDED | MARKET_REOPENED`
- `market_id`
- `payload` (JSON)

## Circuit Breaker Rules

Evaluated every 1 second over a rolling 30-second window:

- Suspend if `bet_count_30s >= 120`
- Suspend if `liability_delta_30s_cents >= 2_000_000`
- Cooldown is 60 seconds
- Reopen only if cooldown elapsed **and** both signals are below thresholds

## In-Memory SQLite Configuration

- Core DSN: `file:core_events?mode=memory&cache=shared`
- Replica DSN pattern: `file:replica_<id>?mode=memory&cache=shared`

Notes:

- Each service instance has its own process-local in-memory DB
- `cache=shared` allows sharing across connections inside a process
- Data is ephemeral and disappears on process exit

## API Contract

### Core

- `GET /stream?from_seq=<n>` (SSE stream)
- `GET /snapshot`
- `POST /internal/events`
- `POST /internal/scenarios/{name}` where `{name}` is `spike` or `normalize`
- `GET /healthz`

### Replica

- `GET /markets`
- `GET /markets/{market_id}`
- `GET /markets/{market_id}/history`
- `GET /replica/status`
- `GET /healthz`

## Data Flow and Consistency

1. Core ingests synthetic/manual bet-flow signals.
2. Core ticker computes 30s metrics and evaluates breaker transitions.
3. State changes emit ordered events (`seq`) persisted in `core.event_log`.
4. Replicas consume pushed SSE events from `from_seq=last_applied_seq+1`.
5. Replica dedupes by `event_id` using `applied_events` table.
6. Replica applies only if `seq == last_applied_seq + 1`.
7. On sequence mismatch, replica fetches `/snapshot`, replaces local state, updates checkpoint, then resumes stream.

### Delivery semantics

- At-least-once delivery from stream/reconnect behavior
- Idempotency via `event_id` dedupe (`INSERT OR IGNORE` path)
- Event ordering enforced by sequence checks

## Trade-offs

- Snapshot-on-gap is simple and robust for take-home scope, but can discard local per-event history continuity across recovery boundaries.
- In-memory SQLite keeps runtime deterministic and fast, but state is non-durable by design.
- SSE is one-way and operationally simple for fanout; if bi-directional flow were needed, WebSockets/gRPC streams would be candidates.

## How to Run

### Prerequisites

- Go 1.22+

### Scripted demo (one command)

```bash
go run ./cmd/demo
```

What it does:

- Starts core + 2 replicas
- Triggers `spike`
- Verifies both replicas converge to `SUSPENDED`
- Triggers `normalize`
- Waits through cooldown and verifies both replicas converge back to `OPEN`

Optional hold mode:

```bash
go run ./cmd/demo -hold
```

### Interactive demo

Run three terminals:

Terminal 1 (core):

```bash
go run ./cmd/core
```

Terminal 2 (replica 1):

```bash
REPLICA_ID=r1 REPLICA_PORT=8081 CORE_BASE_URL=http://127.0.0.1:8080 go run ./cmd/replica
```

Terminal 3 (replica 2):

```bash
REPLICA_ID=r2 REPLICA_PORT=8082 CORE_BASE_URL=http://127.0.0.1:8080 go run ./cmd/replica
```

Example interactive calls:

```bash
curl -X POST http://127.0.0.1:8080/internal/scenarios/spike
curl http://127.0.0.1:8081/markets/market-news-001
curl http://127.0.0.1:8082/markets/market-news-001
curl -X POST http://127.0.0.1:8080/internal/scenarios/normalize
curl http://127.0.0.1:8081/replica/status
curl http://127.0.0.1:8082/replica/status
```

## Testing

Run all tests:

```bash
go test ./...
```

Run vet:

```bash
go vet ./...
```

Run the `event_log` sequence-read benchmark used for index evaluation:

```bash
go run ./cmd/eventlog-bench
```

Benchmark decision and captured results are documented in
[`docs/benchmarks/event_log_seq_reads.md`](docs/benchmarks/event_log_seq_reads.md).

Implemented tests include:

- Unit tests
  - breaker trips on bet-rate threshold
  - breaker trips on liability threshold
  - breaker does not trip below thresholds
  - auto-reopen only after cooldown + normalized metrics
  - duplicate `event_id` is idempotent no-op
- Integration tests
  - single replica convergence
  - two replica convergence
  - forced sequence gap -> snapshot recovery
  - reconnect resumes from checkpoint
  - restart bootstraps from snapshot + stream and converges

## Project Structure

- `cmd/core`: core service binary
- `cmd/replica`: replica service binary
- `cmd/demo`: scripted end-to-end demo runner
- `cmd/eventlog-bench`: event log read-pattern benchmark utility
- `docs/benchmarks`: captured benchmark runs and decisions
- `internal/domain`: shared domain types and breaker logic
- `internal/core`: event log, state machine, SSE API
- `internal/replica`: SSE consumer, dedupe/checkpoint store, read API
- `internal/shared`: shared HTTP helpers

## AI & Tools

Used:

- Codex (GPT-5) for implementation and test scaffolding
- Go standard tooling (`go test`, `go vet`, `gofmt`, `go mod tidy`)

Overridden/adjusted during development:

- SSE stream behavior was adjusted to flush an initial SSE comment immediately so clients establish long-lived stream connections without waiting for the first business event.
