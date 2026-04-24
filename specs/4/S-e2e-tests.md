---
status: shipped
---

# Per-daemon integration tests

Each daemon gets one `<daemon>/integration_test.go` exercising its
HTTP/socket boundary against an in-memory shared DB. No docker, no
mockagent image, no new deps. Fills the gap where unit tests with
mocks diverge from real wiring.

## Shared helpers

`tests/test_utils.go` (package `testutils`), ~150 LOC:

- `NewInstance(t) *Inst` — tmpdir + `store.OpenMem()` + JWT signer +
  registered channels
- `FakeChannel` — implements `chanlib.BotHandler`, records outbound calls
- `FakePlatform` — `httptest.Server` stub for platform REST APIs
- `AssertMessage(db, jid, text)`, `WaitForRow(db, query, timeout)`

## Prerequisite refactor

Extract `container.Runner` interface from `container.Run()` so a
`FakeRunner` can be injected in `gateway/integration_test.go`. ~50 LOC.

## Per-daemon matrix

| Daemon    | File                          | Cases                                                                  |
| --------- | ----------------------------- | ---------------------------------------------------------------------- |
| gated     | `gateway/integration_test.go` | TestPollLoop_RealRun (FakeRunner markers → callback → cursor advances) |
| container | `container/run_test.go`       | Table-driven: docker arg assembly + marker parsing (exec.Command stub) |
| timed     | `tests/microservice_test.go`  | TestCronFiresMessage (active task, `TickOnce`, row appears)            |
| onbod     | `onbod/integration_test.go`   | TestOnboardingFlow, TestOAuthCallback, TestGateRateLimit               |
| dashd     | `dashd/integration_test.go`   | TestMemoryEndpoint, TestGroupList, TestTaskList                        |
| webd      | `webd/integration_test.go`    | TestSlinkWebsocketEcho, TestSlinkMCPBridge                             |
| proxyd    | `proxyd/integration_test.go`  | TestAuthGate_401, TestPubPathOpen, TestReverseProxy                    |
| teled     | `teled/integration_test.go`   | TestBotHandler_Send/Like (FakePlatform stub of Telegram API)           |
| discd     | `discd/integration_test.go`   | TestBotHandler_Send/Like/Post/Delete                                   |
| mastd     | `mastd/integration_test.go`   | TestBotHandler_Send/Like/Post/Delete                                   |
| bskyd     | `bskyd/integration_test.go`   | TestBotHandler_Send/Like/Post/Delete                                   |
| reditd    | `reditd/integration_test.go`  | TestBotHandler_Send/Post/Delete                                        |
| emaid     | `emaid/integration_test.go`   | TestBotHandler_Send (IMAP/SMTP stubs)                                  |
| linkd     | `linkd/integration_test.go`   | TestBotHandler_Send                                                    |
| whapd     | `whapd/src/__tests__/`        | Vitest TS integration against fake WhatsApp endpoint                   |
| ipc       | `tests/integration_test.go`   | TestMCPSocketRoundtrip (ipc.Server on tmp socket, tool call)           |

## Make targets

- `make test` — fast unit tests, unchanged
- `make test-integration` — new, runs integration suite

## Build order

1. `tests/test_utils.go` + `container.Runner` interface — blocking
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
remains only via manual deploy to krons. Documented as a known gap
in `ARCHITECTURE.md`; acceptable trade-off vs building docker-in-docker
CI.
