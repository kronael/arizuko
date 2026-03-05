# autotesting — subsystem test strategy

## Problem

Kanipi has multiple subsystems that interact at runtime: channel adapters,
enrichment pipeline, IPC, container runner, message loop dispatch. When a
bug manifests in production (e.g. voice transcription missing for second
message), there is no automated test to catch the regression or verify the
fix without manually sending Telegram messages and reading logs.

Goal: every subsystem should have tests that run in CI without external
services, and failure scenarios should be reproducible in under 5s.

---

## Test tiers

### Unit — `src/*.test.ts`, `src/mime-handlers/*.test.ts`

Narrow scope, no I/O, fast (<1s each). Mock external calls only
(fetch, spawn, fs). Cover:

- Handler logic (voice, video, whisper client)
- Config parsing and validation
- Formatting, routing helpers
- DB queries (in-memory SQLite via `_initTestDatabase`)

### Integration — `tests/e2e/*.test.ts`

Wire multiple real modules together. Mocked: docker/channels/fs side
effects. Real: DB (in-memory), GroupQueue, mime-enricher, message
formatting. Cover gateway orchestration without docker or network.

### Smoke — `make smoke`

Requires docker. Spawns real containers, checks actual IPC round-trip.
Not run in unit CI, run before releases.

---

## Subsystem coverage targets

### mime-enricher (`src/mime-enricher.test.ts`)

Key scenarios:

- MEDIA_ENABLED=false → no-op
- Pipeline returns lines → appendMessageContent called
- Pipeline returns empty → no write
- Pipeline throws → swallowed, waitForEnrichments resolves
- In-flight race: enrichment completes before waitForEnrichments called →
  appendMessageContent already ran, DB has content, wait returns immediately

The race scenario is what caused voice transcription to silently drop on
second voice messages in active sessions (message fetched before wait,
enrichment finished mid-wait, stale objects piped to container).

### Message loop dispatch (`tests/e2e/message-loop.test.ts`)

Key scenarios:

- New container path: missedMessages re-fetched after waitForEnrichments
- Stdin pipe path: refreshed fetch after waitForEnrichments
- Both paths should include voice label in formatted prompt

To add: e2e test that exercises onMessage → enqueueEnrichment → processGroupMessages
with a mock whisperTranscribe to verify the agent receives transcribed content.

### IPC drain (`src/ipc.test.ts` — to add)

Key scenario: fs.watch fires multiple events for same file →
only one file send, not duplicates. Lock guard prevents concurrent
drainGroupMessages for same group.

### Voice handler (`src/mime-handlers/voice.test.ts`)

Key scenarios:

- auto-detect pass only (no .whisper-language)
- forced language passes: one entry per code
- parallel passes: Promise.allSettled, failures don't block
- label format: `voice/auto→{detected}` vs `voice/{forced}`
- all passes fail → empty lines → no write

---

## Test design principles

**Test the feature, not the fix.** When a bug is found, write a test that
verifies the correct behavior — not one that merely reproduces the broken
state. The test should pass with the fix and fail without it.

**Mock only at system boundaries.** Whisper is an external HTTP service →
mock fetch. Docker is external → mock spawn. SQLite in-memory is fine as
real I/O. fs.watch behavior is non-deterministic → mock it.

**Fake timers for timeout/race scenarios.** vi.useFakeTimers lets you
advance time deterministically, test 60s timeouts in milliseconds, and
reproduce race conditions reliably.

**One test file per module.** Colocate with source (`src/foo.test.ts`).
Integration tests live in `tests/e2e/` since they span multiple modules.

**MEDIA_ENABLED guard in enricher tests.** Always test the disabled path —
it's the default in test config and easy to break silently.

---

## CI integration

`make test` runs all unit + integration tests (no docker needed, <10s).
`make smoke` runs docker-dependent tests separately.

Suggested additions:

1. `src/mime-enricher.test.ts` — enrichment pipeline + race condition ✓ (shipped)
2. `src/ipc.test.ts` — drain serialization lock
3. `tests/e2e/voice-enrichment.test.ts` — full onMessage→formatMessages roundtrip
