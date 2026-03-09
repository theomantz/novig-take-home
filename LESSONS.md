# LESSONS

Repo-specific lessons for `novig-take-home`.
Capture repeat issues, root causes, and proven fixes for this codebase.

## Entry Format

```md
### YYYY-MM-DD - Short title
- Context: repo/task
- Symptom: what went wrong
- Root cause: why it happened
- Fix: what resolved it
- Prevention: checks or workflow changes to avoid recurrence
```

## Lessons

### 2026-03-06 - SSE clients can block until first flush
- Context: core `/stream` SSE replication behavior
- Symptom: replica stream connects appeared stalled because the client blocked waiting for first bytes.
- Root cause: headers were set but no bytes were written/flushed until first business event or delayed heartbeat.
- Fix: write an initial SSE comment frame (`:connected`) and flush immediately after headers.
- Prevention: in SSE handlers, always flush an initial frame at connection setup.

### 2026-03-07 - Shared-cache in-memory SQLite needs unique test DB names
- Context: integration/unit tests for core and replica stores
- Symptom: tests can interfere with one another when reused in-memory DB names share state unexpectedly.
- Root cause: DSNs use `mode=memory&cache=shared`, which shares data by database name inside a process.
- Fix: generate unique DB names per test (for example with `time.Now().UnixNano()`).
- Prevention: never hardcode a reusable DB name for concurrent/parallel tests.

### 2026-03-07 - Event apply ordering must stay strict even with dedupe
- Context: replica `processEvent` sequencing and gap recovery
- Symptom: relaxing ordering checks can silently skip missing events and diverge from core state.
- Root cause: at-least-once delivery can include duplicates and reconnect boundaries; dedupe alone is not enough.
- Fix: keep expected-sequence validation (`last_applied_seq + 1`) and trigger snapshot refresh on gaps.
- Prevention: for replication changes, test duplicate events and forced gap recovery in the same PR.

### 2026-03-07 - SQLite INTEGER PRIMARY KEY often already covers seq max/reverse lookups
- Context: core `event_log(seq INTEGER PRIMARY KEY)` performance discussion
- Symptom: concern that `MAX(seq)` and descending latest-seq lookups require an extra `seq DESC` index.
- Root cause: assumption overlooked rowid behavior behind `INTEGER PRIMARY KEY`.
- Fix: benchmark baseline first and add directional indexes only with measured wins.
- Prevention: require benchmark evidence before adding indexes on primary-key `seq` in this repo.

### 2026-03-08 - Stream EOF should not pollute replica `last_error`
- Context: replica stream resilience hardening (`StreamIdleTimeout`, reconnect status).
- Symptom: `/replica/status` could report `last_error="EOF"` even after successful reconnect/apply because routine stream closes were treated as failures.
- Root cause: `consumeStream` returning `io.EOF` flowed through generic disconnect handling that always updated `last_error`.
- Fix: treat `io.EOF` as a normal reconnect signal (log + backoff + reconnect) without setting `last_error`.
- Prevention: reserve `last_error` for actionable failures (timeout, decode/validation, non-200 responses), not expected stream lifecycle events.

### 2026-03-09 - Nix and host Go versions can diverge during docs validation
- Context: README command verification for Nix and native tooling paths.
- Symptom: `nix develop -c go version` and `go version` reported different Go versions.
- Root cause: Nix shells pin their own toolchain independently from host-installed Go.
- Fix: validate each path against the documented minimum (`Go 1.22+`) instead of expecting identical version strings.
- Prevention: when validating README setup commands, treat Nix vs host Go version drift as expected unless either path violates minimum requirements.

### 2026-03-09 - Cancel demo run context before deferred HTTP shutdown
- Context: `cmd/demo` scripted convergence shutdown sequence.
- Symptom: demo exit could emit late heartbeat errors (and appear stuck/noisy) right after successful completion.
- Root cause: signal context cancellation happened in a deferred call after HTTP server shutdown had already started.
- Fix: call `stop()` before returning in non-hold mode so replica/core loops exit before deferred server shutdown.
- Prevention: for multi-service demos/tests, cancel shared run contexts before shutting down servers that those goroutines depend on.
