## Summary

Implements a full Go-based core/replica replication system for sportsbook market-risk state, using SSE push replication with at-least-once delivery semantics, idempotent dedupe, and snapshot gap recovery.

## What Changed

- Added core service (`cmd/core`, `internal/core`):
  - In-memory market state machine with 1s breaker evaluation over a 30s rolling window.
  - Ordered event emission (`seq`, `event_id`) and structured logs.
  - In-memory SQLite `event_log` persistence.
  - SSE stream endpoint (`/stream?from_seq=`) with replay + live fanout.
  - Snapshot endpoint and internal scenario/injection endpoints.
- Added replica service (`cmd/replica`, `internal/replica`):
  - SSE consumer from core.
  - Idempotent dedupe via `applied_events(event_id PK)`.
  - Strict in-order sequence enforcement.
  - Gap recovery by snapshot replacement + checkpoint reset.
  - In-memory SQLite checkpoint/dedupe tables.
  - Read-only replica APIs (`/markets`, `/markets/{id}`, `/history`, `/replica/status`).
- Added scripted demo command (`cmd/demo`):
  - Boots core + two replicas.
  - Triggers spike and verifies convergence to `SUSPENDED`.
  - Triggers normalize and verifies auto-reopen to `OPEN` after cooldown.
- Added tests:
  - Breaker unit tests for thresholds and cooldown reopen behavior.
  - Idempotency unit test for duplicate `event_id` no-op.
  - Integration tests for single/multi replica convergence, forced gap recovery, reconnect checkpoint resume, restart bootstrap from snapshot.
- Added full README with architecture, API contract, consistency/trade-offs, real-world use case, run/demo/test instructions, and AI/tools disclosure.
- Added Nix dev shell (`flake.nix`, `flake.lock`) for reproducible tooling.

## Why

The take-home requires a realistic distributed replication design centered on sportsbook risk controls and volatile bet-flow handling, without odds/pricing logic. This implementation focuses on deterministic replication behavior and operationally explainable consistency guarantees.

## Expected Impact

- Demonstrates authoritative core state with replicated read followers.
- Shows convergence across multiple replicas under spike/suspend/reopen transitions.
- Provides deterministic failure recovery behavior via snapshot fallback on sequence gaps.

## Risks

- Snapshot recovery replaces local market history continuity on the replica (state correctness prioritized over complete local historical continuity).
- In-memory SQLite is intentionally ephemeral; all state resets on process restart.
- SSE fanout in this scope is in-process and simple; high-scale production fanout would need stronger backpressure and delivery observability.

## Validation Performed

- `go test ./...`
- `go vet ./...`
- `go test -race ./...`
- `nix develop -c go test ./...`
- `nix develop -c go vet ./...`
- `go run ./cmd/demo` (verified suspend convergence + post-cooldown reopen convergence)

## Rollback / Backout

- Revert commit `9dd64df` to remove all introduced core/replica/demo code and return to initial repository state.

## Follow-ups

- Persist replica event history in SQLite if historical continuity after snapshot recovery is required.
- Add auth/rate-limiting around internal injection/scenario endpoints for non-demo environments.
- Add load/soak tests for SSE subscriber fanout and reconnect storm behavior.
