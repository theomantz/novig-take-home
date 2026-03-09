# Novig Take-Home: Core/Replica Market Risk Replication (Go)

This repository implements a distributed core/replica system for sportsbook market-risk controls.
The breaker logic is based on bet-flow signals (bet velocity and liability acceleration), not odds/pricing.

## Table of Contents

- [Architecture Overview](#architecture-overview)
- [Replication Invariants](#replication-invariants)
- [Circuit Breaker Rules](#circuit-breaker-rules)
- [Prerequisites](#prerequisites)
- [Detailed Setup](#detailed-setup)
- [Run the System](#run-the-system)
- [Test Script](#test-script)
- [Manual API Testing with curl](#manual-api-testing-with-curl)
- [API Surface](#api-surface)
- [Configuration](#configuration)
- [Project Layout](#project-layout)

## Architecture Overview

### Core (`cmd/core`)

- Owns authoritative in-memory market state.
- Evaluates breaker transitions every 1 second over a 30-second rolling window.
- Emits replication events with strict, gap-free `seq` ordering.
- Persists events to in-memory SQLite (`event_log`) before broadcasting.
- Serves:
  - SSE stream (`GET /stream?from_seq=<n>`)
  - snapshot (`GET /snapshot`)
  - replica health (`GET /replicas/status`)
  - internal signal/scenario endpoints.

### Replica (`cmd/replica`)

- Bootstraps from core snapshot.
- Consumes SSE (`/stream`) from `last_applied_seq + 1`.
- Applies at-least-once delivery with idempotent dedupe by `event_id`.
- Enforces strict sequential apply (`evt.seq == last_applied_seq + 1`).
- On any sequence gap, refreshes from `/snapshot` and resumes from checkpoint.
- Serves read-only APIs (`/markets`, `/replica/status`, etc.).

### Demo (`cmd/demo`)

- Starts one core and two replicas.
- Triggers `spike` then `normalize` scenarios.
- Verifies convergence to `SUSPENDED` and then back to `OPEN`.

## Replication Invariants

These are intentional design constraints in this repo:

1. Core `seq` ordering is strictly increasing and gap-free.
2. Core persists events before SSE broadcast.
3. If core persistence fails, authoritative state and `lastSeq` do not advance.
4. Replica dedupes on `event_id`.
5. Replica only applies `evt.seq == last_applied_seq + 1`.
6. Sequence mismatch is treated as a gap and triggers snapshot recovery.

## Circuit Breaker Rules

Evaluated every second over the rolling 30-second window:

- Suspend if `bet_count_30s >= 120`.
- Suspend if `liability_delta_30s_cents >= 2_000_000`.
- Cooldown duration: 60 seconds.
- Reopen only when cooldown elapsed and both metrics are back below thresholds.

## Prerequisites

- Recommended: [Nix](https://nixos.org/download/) with flakes enabled.
- Go `1.22+` (fallback path if you are not using Nix).
- `curl` for manual endpoint validation.
- Optional: `jq` for pretty-printing JSON responses.

## Detailed Setup

### 1. Clone and enter the repo

```bash
git clone <your-fork-or-origin-url>
cd novig-take-home
```

### 2. Preferred tooling path (Nix)

```bash
nix develop
```

Inside the shell, verify toolchain:

```bash
go version
```

### 3. Fallback tooling path (native Go)

If Nix is unavailable, install Go `1.22+` and run commands directly.

```bash
go version
```

### 4. Baseline validation

Run the repository test script:

```bash
scripts/test.sh
```

This runs, in order:

1. `go test ./...`
2. `go vet ./...`
3. `go test -race ./...`

## Run the System

### Option A: scripted convergence demo

```bash
go run ./cmd/demo
```

Keep services alive after verification:

```bash
go run ./cmd/demo -hold
```

### Option B: manual multi-terminal run

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

## Test Script

The repository includes `scripts/test.sh`:

```bash
scripts/test.sh --help
```

Options:

- `--no-nix`: run commands directly instead of `nix develop -c ...`
- `--with-demo`: also run `go run ./cmd/demo` after tests

Example with demo:

```bash
scripts/test.sh --with-demo
```

Optional benchmark command (used for event-log sequence read behavior):

```bash
go run ./cmd/eventlog-bench
```

## Manual API Testing with curl

Assuming core is on `:8080`, replicas on `:8081` and `:8082`.

### 0. Export convenience variables

```bash
CORE=http://127.0.0.1:8080
R1=http://127.0.0.1:8081
R2=http://127.0.0.1:8082
MID=market-news-001
```

### 1. Health checks

```bash
curl -s "$CORE/healthz"
curl -s "$R1/healthz"
curl -s "$R2/healthz"
```

### 2. Inspect initial state

First, ensure both replicas are bootstrapped and connected:

```bash
curl -s "$R1/replica/status"
curl -s "$R2/replica/status"
```

Then inspect market/snapshot state:

```bash
curl -s "$CORE/snapshot"
curl -s "$R1/markets/$MID"
curl -s "$R2/markets/$MID"
```

### 3. Observe replication stream directly (optional)

Run this in a separate terminal:

```bash
curl -N "$CORE/stream?from_seq=1"
```

Expected stream frame pattern:

- initial `:connected`
- heartbeat comments `:keepalive`
- replication events:
  - `event: replication`
  - `data: {"seq":...,"event_id":"...",...}`

### 4. Trigger suspension scenario

```bash
curl -s -X POST "$CORE/internal/scenarios/spike"
```

Check both replicas converged to `SUSPENDED`:

```bash
curl -s "$R1/markets/$MID"
curl -s "$R2/markets/$MID"
```

### 5. Check replica and core replication status

```bash
curl -s "$R1/replica/status"
curl -s "$R2/replica/status"
curl -s "$CORE/replicas/status"
```

### 6. Trigger normalize and wait for reopen after cooldown

```bash
curl -s -X POST "$CORE/internal/scenarios/normalize"
```

Poll until both replicas return to `OPEN` (cooldown is 60 seconds):

```bash
for i in {1..90}; do
  s1=$(curl -s "$R1/markets/$MID" | jq -r '.status')
  s2=$(curl -s "$R2/markets/$MID" | jq -r '.status')
  echo "tick=$i r1=$s1 r2=$s2"
  if [[ "$s1" == "OPEN" && "$s2" == "OPEN" ]]; then
    echo "both replicas reopened"
    break
  fi
  sleep 1
done
```

### 7. Inject custom signal payload (manual event input)

```bash
curl -s -X POST "$CORE/internal/events" \
  -H 'Content-Type: application/json' \
  -d '{
    "market_id": "market-news-001",
    "bet_count": 10,
    "stake_cents": 25000,
    "liability_delta_cents": 50000
  }'
```

### 8. Read history from a replica

```bash
curl -s "$R1/markets/$MID/history"
```

## API Surface

### Core

- `GET /stream?from_seq=<n>`
- `GET /snapshot`
- `GET /replicas/status`
- `POST /internal/events`
- `POST /internal/replicas/heartbeat`
- `POST /internal/scenarios/{spike|normalize}`
- `GET /healthz`

### Replica

- `GET /markets`
- `GET /markets/{market_id}`
- `GET /markets/{market_id}/history`
- `GET /replica/status`
- `GET /healthz`

## Configuration

### Core env vars

| Variable | Default | Description |
| --- | --- | --- |
| `CORE_PORT` | `8080` | Core HTTP port |
| `CORE_DB_NAME` | `core_events` | In-memory SQLite DB name (`file:<name>?mode=memory&cache=shared`) |
| `DEFAULT_MARKET_ID` | `market-news-001` | Default market ID used by scenarios/signals |
| `REPLICA_STALE_AFTER_MS` | `5000` | Heartbeat age threshold for `stale` |
| `REPLICA_OFFLINE_AFTER_MS` | `15000` | Heartbeat age threshold for `offline` |

### Replica env vars

| Variable | Default | Description |
| --- | --- | --- |
| `REPLICA_ID` | `replica-1` | Replica identifier |
| `REPLICA_PORT` | `8081` | Replica HTTP port |
| `CORE_BASE_URL` | `http://127.0.0.1:8080` | Core base URL |
| `REPLICA_RECONNECT_BACKOFF_INITIAL_MS` | `500` | Initial reconnect backoff |
| `REPLICA_RECONNECT_BACKOFF_MAX_MS` | `5000` | Reconnect backoff cap |
| `REPLICA_SNAPSHOT_TIMEOUT_MS` | `3000` | Snapshot fetch timeout |
| `REPLICA_STREAM_IDLE_TIMEOUT_MS` | `45000` | Stream idle timeout before reconnect |
| `REPLICA_HEARTBEAT_INTERVAL_MS` | `2000` | Heartbeat cadence to core |
| `REPLICA_HEARTBEAT_TIMEOUT_MS` | `2000` | Heartbeat POST timeout |

## Project Layout

- `cmd/core`: core binary
- `cmd/replica`: replica binary
- `cmd/demo`: end-to-end demo runner
- `cmd/eventlog-bench`: event log read benchmark harness + findings guide
- `internal/domain`: shared types + breaker logic
- `internal/core`: event log + breaker loop + SSE + snapshot + replica health
- `internal/replica`: stream consumer + dedupe/checkpoint store + read APIs
- `cmd/eventlog-bench/README.md`: canonical benchmark notes/results
- `docs/benchmarks/event_log_seq_reads_run_2026-03-07.txt`: raw benchmark output snapshot
