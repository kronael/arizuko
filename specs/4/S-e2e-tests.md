---
status: shipped
---

# Per-daemon integration tests

Each daemon gets one integration test exercising its HTTP/socket
boundary against an in-memory shared DB — `<daemon>/integration_test.go`
for daemons whose surface is HTTP/auth (gated, onbod, dashd, webd,
proxyd), or `<daemon>/bothandler_test.go` for adapters whose surface
is the `chanlib.BotHandler` interface against a stubbed platform API.
No docker, no mockagent image, no new deps. Fills the gap where unit
tests with mocks diverge from real wiring.

## Shared helpers

`tests/testutils/testutils.go` (package `testutils`), ~320 LOC:

- `NewInstance(t) *Inst` — tmpdir SQLite (migrated) + JWT secret +
  empty `chanreg.Registry`
- `FakeChannel` — implements `core.Channel` + `core.Socializer`, records
  Send/SendFile/Like/Post/Delete/Typing for assertion
- `FakePlatform` — configurable `httptest.Server` keyed by `METHOD /path`
- `AssertMessage(db, jid, substr)`, `WaitForRow(db, query, args, timeout)`

## Prerequisite refactor

Extract `container.Runner` interface from `container.Run()` so a
`FakeRunner` can be injected in `gateway/integration_test.go`. ~50 LOC.

## Per-daemon matrix

| Daemon    | File                          | Cases                                                                  |
| --------- | ----------------------------- | ---------------------------------------------------------------------- |
| gated     | `gateway/integration_test.go` | TestPollLoop_RealRun (FakeRunner markers → callback → cursor advances) |
| container | `container/run_test.go`       | Table-driven: docker arg assembly + marker parsing (exec.Command stub) |
| timed     | `tests/microservice_test.go`  | TestCronFiresMessage (active task, claim+insert, row appears)          |
| onbod     | `onbod/integration_test.go`   | TestOnboardingFlow, TestOAuthCallback, TestGateRateLimit               |
| dashd     | `dashd/integration_test.go`   | TestMemoryEndpoint, TestGroupList, TestTaskList                        |
| webd      | `webd/integration_test.go`    | TestSlinkWebsocketEcho, TestSlinkMCPBridge                             |
| proxyd    | `proxyd/integration_test.go`  | TestAuthGate_Unauthorized, TestPubPathOpen, TestReverseProxy           |
| teled     | `teled/bothandler_test.go`    | TestBotHandler_Send/Like (FakePlatform stub of Telegram API)           |
| discd     | `discd/bothandler_test.go`    | TestBotHandler_Send/Like/Quote/Delete                                  |
| mastd     | `mastd/bothandler_test.go`    | TestBotHandler_Send/Like/Post/Delete                                   |
| bskyd     | `bskyd/bothandler_test.go`    | TestBotHandler_Send/Like/Post/Delete                                   |
| reditd    | `reditd/bothandler_test.go`   | TestBotHandler_Send/Post/Delete                                        |
| emaid     | `emaid/bothandler_test.go`    | TestBotHandler_Send (IMAP/SMTP stubs)                                  |
| linkd     | `linkd/bothandler_test.go`    | TestBotHandler_Send                                                    |
| whapd     | `whapd/src/__tests__/`        | bun:test TS integration against stubbed WASocket                       |
| ipc       | `tests/integration_test.go`   | TestMCPSocketRoundtrip (ipc.Server on tmp socket, tool call)           |

## Make targets

- `make test` — runs unit + integration in one pass; full suite is fast
  enough that no separate `test-integration` gate is needed.

## Build order

1. `tests/testutils/testutils.go` + `container.Runner` interface — blocking
2. Fan out four buckets in parallel:
   - A: gated, container, timed, MCP round-trip
   - B: onbod, dashd
   - C: webd, proxyd
   - D: adapters (teled, discd, mastd, bskyd, reditd, emaid, linkd, whapd)

## Why not docker-based e2e

Per-container docker e2e (mockagent binary + compose harness) was the
earlier aspirational scope. Most of what it would test is already
covered by unit tests with mocks (`gateway_test.go`, `microservice_test.go`).
The genuine gaps — `container.Run()` exec path, MCP socket round-trip —
are addressed here without new infra or deps.

Full multi-daemon swarm coverage (systemd + compose + real docker)
remains only via manual deploy to krons; acceptable trade-off vs
building docker-in-docker CI.
