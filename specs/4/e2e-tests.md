---
status: unshipped
---

# E2E Integration Tests

`gateway/gateway_test.go` is pure unit tests — `store.OpenMem()` +
`mockChannel`, never calls `container.Run()`. The full path — message
→ queue → docker → output markers → channel send — is untested.

Fixes by replacing the real agent image with a mock binary that
speaks the same stdin/stdout protocol.

## Mock agent protocol

Reads `container.Input` JSON from stdin. Parses last line of `prompt`
for a command prefix. Writes output wrapped in
`---ARIZUKO_OUTPUT_START---`/`---ARIZUKO_OUTPUT_END---`.

| Prompt prefix    | Behavior                                                    |
| ---------------- | ----------------------------------------------------------- |
| `echo:<text>`    | success, `result=<text>`, new session id                    |
| `delay:<ms>`     | sleep, then `echo:ok`                                       |
| `empty`          | `{"status":"success","result":null}` — tests no-output path |
| `error:<text>`   | `{"status":"error","error":"<text>"}`, exit 1               |
| `session_error`  | no markers, exit 0 — triggers session eviction              |
| `heartbeat:<ms>` | emit heartbeats then `echo:ok` — tests idle timer reset     |
| `mcp:<tool>`     | connect `/workspace/ipc/gated.sock` via socat, call tool    |
| `file:<relpath>` | write file, call MCP `send_file`                            |

## Layout

```
tests/
  e2e/
    e2e_test.go     //go:build e2e
    harness.go      start gated, return client
    channel.go      FakeChannel implementing core.Channel
  mockagent/
    main.go         ~150 LOC
    Dockerfile      alpine + mockagent binary
```

`make e2e` runs `go test -tags e2e ./tests/e2e/...`. Not in `make test`.

## Test cases

TC-01 echo, TC-02 empty result, TC-03 session continuity, TC-04
session error recovery, TC-05 agent error + cursor rollback, TC-06
concurrent container limit, TC-07 idle timeout, TC-08 heartbeat
resets, TC-09 MCP send_message, TC-10 send_file, TC-11 multi-group
routing, TC-12 scheduler injection, TC-13 reply threading.

## Why

Unit tests never hit: `container.Run()`, `queue.GroupQueue` under
concurrency, `ipc.ServeMCP`, real cursor rollback, heartbeat timer
reset, session eviction via SDK `error_during_execution`. TC-01,
TC-04, TC-05 catch the most production failures.
