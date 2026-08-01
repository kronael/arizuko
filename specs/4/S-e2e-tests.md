---
status: shipped
---

# Per-daemon integration tests

Each daemon gets one integration test exercising its HTTP/socket
boundary against an in-memory DB. The file name encodes which boundary:
`<daemon>/integration_test.go` for daemons whose surface is HTTP/auth
(routd, onbod, dashd, webd, proxyd), `<daemon>/bothandler_test.go` for
adapters whose surface is the `chanlib.BotHandler` interface against a
stubbed platform API. No docker, no mockagent image, no new deps.
Fills the gap where unit tests with mocks diverge from real wiring.

## Shared helpers

`tests/testutils/testutils.go` (package `testutils`), ~320 LOC:

- `NewInstance(t) *Inst` — tmpdir SQLite (migrated) + JWT secret +
  empty `chanreg.Registry`
- `FakeChannel` — implements `core.Channel` + `core.Socializer`, records
  Send/SendFile/Like/Post/Delete/Typing for assertion
- `FakePlatform` — configurable `httptest.Server` keyed by `METHOD /path`
- `AssertMessage(db, jid, substr)`, `WaitForRow(db, query, args, timeout)`

`container.Runner` is an interface (`container/runner.go`) precisely so
a `FakeRunner` can be injected — the docker exec path is the one thing
a boundary test cannot reach any other way.

## Make targets

- `make test` — runs unit + integration in one pass; the full suite is
  fast enough that no separate `test-integration` gate is needed.
- `make test-e2e` — the webd route-token release gate, a narrower thing
  than this spec's per-daemon boundary tests.

## Why not docker-based e2e

Per-container docker e2e (mockagent binary + compose harness) was the
earlier aspirational scope. Most of what it would test is already
covered by unit tests with mocks. The genuine gaps —
`container.Run()`'s exec path and the MCP socket round-trip — are
addressed here without new infra or deps.

Full multi-daemon swarm coverage (systemd + compose + real docker)
remains only via manual deploy to krons; acceptable trade-off vs
building docker-in-docker CI.
