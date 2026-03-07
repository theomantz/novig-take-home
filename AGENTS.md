# AGENTS.md

Repo-specific instructions for `/Users/theo/development/novig-take-home`.
Global rules still apply.

## Repo Scope

- Go module: `novig-take-home`
- Binaries:
  - `cmd/core`: authoritative state owner and event producer
  - `cmd/replica`: follower that serves read APIs from replicated state
  - `cmd/demo`: scripted end-to-end convergence demo
- Package boundaries:
  - `internal/domain`: shared types + circuit-breaker rules
  - `internal/core`: event log, breaker evaluation loop, SSE stream, snapshot API
  - `internal/replica`: stream consumer, dedupe/checkpoint store, read handlers

## Replication Invariants (Do Not Break)

1. Core `seq` ordering is strictly increasing and gap-free.
2. Core must persist events before broadcasting them to SSE subscribers.
3. If core persistence fails, do not advance `lastSeq` or mutate authoritative market state.
4. Replica dedupes on `event_id`; duplicate events are no-op applies.
5. Replica applies only when `evt.seq == last_applied_seq + 1`.
6. Any sequence mismatch is treated as a gap; replica refreshes from `/snapshot`, replaces in-memory state, updates checkpoint, then resumes from `last_applied_seq + 1`.

## SSE Contract Guardrails

- Keep `GET /stream?from_seq=<n>` semantics (`from_seq >= 1`).
- Preserve stream frame shape:
  - `event: replication`
  - `data: <json ReplicationEvent>`
- Always write and flush an initial SSE frame after headers (currently `:connected`).
- Preserve periodic heartbeat comments (`:keepalive`) for long-lived connections.
- Backlog replay and live apply order must remain ascending by `seq`.

## SQLite and Test Isolation Rules

- Keep in-memory SQLite DSNs with `mode=memory&cache=shared`.
- Maintain current DSN patterns:
  - core: `file:<name>?mode=memory&cache=shared`
  - replica: `file:replica_<id>?mode=memory&cache=shared`
- In tests, use unique DB names (for example, append `time.Now().UnixNano()`) to avoid shared-cache collisions.
- Replica `CommitAppliedEvent` must remain transactional for dedupe + checkpoint consistency.

## API and Behavior Expectations

- Core internal APIs:
  - `POST /internal/events`
  - `POST /internal/scenarios/{spike|normalize}`
- Replica API surface is read-only:
  - `GET /markets`
  - `GET /markets/{market_id}`
  - `GET /markets/{market_id}/history`
  - `GET /replica/status`
- Keep default market ID behavior (`market-news-001`) unless changing docs/tests/contracts together.

## Required Validation Before PR

Run from repo root. Prefer Nix-managed tooling:

- `nix develop -c go test ./...`
- `nix develop -c go vet ./...`
- `nix develop -c go test -race ./...`

For replication behavior changes, also run:

- `nix develop -c go run ./cmd/demo`

## Change Hygiene

- If you change endpoints, env vars, breaker thresholds/timings, or run/test commands, update `README.md` in the same PR.
- For logic changes, add or update tests in the touched package.
- Replication flow changes should include at least one integration-style check under `internal/replica/integration_test.go`.
