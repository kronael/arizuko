# E2E Integration Tests

## Background

The existing `gateway/gateway_test.go` tests are pure unit tests. They use
`store.OpenMem()` for SQLite and `mockChannel` for the channel interface. They
never call `container.Run()`. They test routing logic, cursor management, and
command dispatch, but the full path — message in store → queue → docker → output
markers → channel send — is completely untested.

This means the following failure modes are invisible to CI:

- `container.Run()` returns an error (docker not running, wrong image, bad stdin)
- Output markers missing or malformed → "no parseable output from container"
- `error_during_execution` subtype from SDK → session eviction logic not exercised
- `hadOutput=false` path → silent "no output delivered" warning never caught
- MCP socket not created in time → agent can't call tools, exits with error
- Session file missing → SDK returns `error_during_execution` on resume
- `store.MarkChatErrored` / cursor rollback on partial failure not validated
- Concurrent container limit enforced correctly under load

The e2e tests replace the real agent image with a mock binary that speaks the
exact same protocol but runs instantly with predefined behavior.

---

## Mock Agent Protocol

### stdin format

Same `container.Input` struct that `container.Run()` marshals and writes:

```json
{
  "prompt": "echo:hello world",
  "sessionId": "sess-abc",
  "groupFolder": "testgrp",
  "chatJid": "test:jid1",
  "assistantName": "testbot",
  "grants": ["send_message(*)"]
}
```

The mock reads the full JSON from stdin and inspects the `prompt` field for a
**command prefix** (`<cmd>:<args>`). If the prompt contains no recognized prefix,
it defaults to `echo:ok`.

### stdout protocol

Output must be wrapped in markers, same as the real agent:

```
---ARIZUKO_OUTPUT_START---
{"status":"success","result":"hello world","newSessionId":"mock-sess-1"}
---ARIZUKO_OUTPUT_END---
```

For heartbeat (streaming keep-alive), the real agent writes:

```
---ARIZUKO_OUTPUT_START---
{"heartbeat":true}
---ARIZUKO_OUTPUT_END---
```

The runner recognizes the heartbeat: it resets the idle timer but does not
call `OnOutput`. Tests that verify idle timeout reset use this.

### Supported commands

| Prompt prefix    | Mock behavior                                                                                  |
| ---------------- | ---------------------------------------------------------------------------------------------- |
| `echo:<text>`    | Emit success output with `result=<text>`, `newSessionId=mock-sess-<n>`                         |
| `delay:<ms>`     | Sleep `<ms>` milliseconds, then emit `echo:ok`                                                 |
| `empty`          | Emit `{"status":"success","result":null}` — tests "no output delivered" path                   |
| `error:<text>`   | Emit `{"status":"error","result":null,"error":"<text>"}`, exit 1                               |
| `session_error`  | Emit no output markers, exit 0 — the runner sees no parseable output; store will evict session |
| `heartbeat:<ms>` | Emit heartbeat every `<ms>`, then after 2x`<ms>` emit `echo:ok` — tests idle timer reset       |
| `mcp:<tool>`     | Connect to `/workspace/ipc/gated.sock` via socat, call `<tool>`, emit `result=<tool_result>`   |
| `file:<relpath>` | Write a file to the group dir at `<relpath>`, call MCP `send_file` tool, emit success          |

Command parsing reads only the **last line** of the prompt to avoid being
confused by gateway-injected annotations (`<clock>`, diary, etc.).

### Session ID generation

The mock emits `"newSessionId":"mock-sess-<unix_nano>"` on every successful
run that doesn't carry an existing session. Tests that verify session continuity
can assert the stored session ID matches.

### MCP calls in the mock

For `mcp:` and `file:` commands, the mock binary needs to talk to the MCP socket.
The simplest approach: shell out to `socat` and write a raw MCP JSON-RPC request.
The mock binary is Go, so it can use the `mcp-go` client directly.

The MCP socket path is `/workspace/ipc/gated.sock`. In the test environment this
is a real unix socket started by `ipc.ServeMCP` before `docker run`.

---

## Test Infrastructure

### Layout

```
tests/
  e2e/
    e2e_test.go          # test cases (build tag: e2e)
    harness.go           # test harness: start gated, return client
    channel.go           # fake channel: collects sent messages, implements core.Channel
  mockagent/
    main.go              # mock agent binary (~150 LOC)
    Dockerfile           # scratch or alpine; COPY mock binary
```

### Build tag

All e2e tests carry `//go:build e2e` so `make test` (fast, no docker) does not
run them. `make e2e` runs `go test -tags e2e ./tests/e2e/...`.

### Harness design

```go
type Harness struct {
    cfg     *core.Config
    store   *store.Store
    gw      *gateway.Gateway
    channel *FakeChannel
    dataDir string
    ctx     context.Context
    cancel  context.CancelFunc
}

func NewHarness(t *testing.T, image string) *Harness
```

`NewHarness`:

1. Creates `t.TempDir()` as data dir.
2. Opens `store.Open(dataDir + "/store")`.
3. Builds `core.Config` with:
   - `Image = image` (default `arizuko-mock-agent:test`)
   - `MaxContainers = 2`
   - `PollInterval = 50ms`
   - `IdleTimeout = 200ms` (short for timeout tests)
   - `DataDir = dataDir`
   - `GroupsDir = dataDir + "/groups"`
   - `HostAppDir = repoRoot()` (needed for `BuildMounts` → `ant/skills/self/MIGRATION_VERSION`)
4. Creates `gateway.New(cfg, store)`.
5. Attaches `FakeChannel`.
6. Registers a default group (JID `"test:jid1"`, folder `"testgrp"`).
7. Starts `gw.Run(ctx)` in a goroutine. Returns `*Harness`.

`t.Cleanup` cancels context and calls `gw.Shutdown()`.

`repoRoot()` walks up from the test file until it finds `go.mod`.

### FakeChannel

```go
type FakeChannel struct {
    mu     sync.Mutex
    sent   []SentMsg
    notify chan struct{}
}

type SentMsg struct {
    JID     string
    Text    string
    ReplyTo string
}
```

Implements `core.Channel`. `Send()` appends to `sent`, signals `notify`.

```go
func (f *FakeChannel) WaitForMessage(t *testing.T, timeout time.Duration) SentMsg
```

Blocks on `notify` until a message arrives or timeout. Calls `t.Fatal` on
timeout. Tests use this instead of `time.Sleep`.

### Injecting messages

```go
func (h *Harness) Send(content string) string // returns message ID
```

Calls `store.PutMessage` directly. The gateway's poll loop will pick it up
within `PollInterval` (50ms).

### Making the mock image

`make e2e-image` target:

```makefile
e2e-image:
    CGO_ENABLED=0 GOOS=linux go build -o tests/mockagent/mockagent ./tests/mockagent
    docker build -t arizuko-mock-agent:test tests/mockagent/
```

`Dockerfile`:

```dockerfile
FROM alpine:3.20
COPY mockagent /mockagent
ENTRYPOINT ["/mockagent"]
```

The mock binary uses no CGO and needs no node/npm. It runs as-is in alpine.

---

## Test Cases

### TC-01: Basic echo (happy path)

```
Given: group registered, FakeChannel attached
When:  h.Send("echo:hello world")
Then:  FakeChannel receives message containing "hello world" within 2s
       store.GetSession("testgrp","") returns non-empty session ID
```

Validates: output markers parsed, result delivered, session stored.

### TC-02: Empty result

```
Given: group registered
When:  h.Send("empty")
Then:  FakeChannel receives NO message within 500ms
       gateway log contains "agent completed with no output delivered"
       store cursor advances (no retry)
```

Validates: `hadOutput=false` path does not retry, cursor still advances.

### TC-03: Session continuity

```
Given: TC-01 ran, store has session ID
When:  h.Send("echo:second message")
Then:  mock receives sessionId in stdin JSON equal to prior session ID
       FakeChannel receives "second message"
```

Validates: `store.GetSession` → `container.Input.SessionID` chain.

### TC-04: Session error recovery

```
Given: store has session "bad-sess" set for testgrp
When:  h.Send("session_error")   // mock exits 0 with no output
Then:  gateway returns Output{Error: "no parseable output"}
       store.GetSession("testgrp","") returns ""  // session evicted
When:  h.Send("echo:retry")
Then:  mock receives empty sessionId (fresh session)
       FakeChannel receives "retry"
```

Validates: error + no-output → session eviction → next message gets fresh session.

### TC-05: Agent error with partial output

```
Given: group registered
When:  h.Send("error:something failed")
Then:  FakeChannel receives NO agent reply
       store.IsChatErrored("test:jid1") == true
       cursor rolled back to before the message
When:  h.Send("echo:retry after error")
Then:  FakeChannel receives "retry after error"   // recovered
       store.IsChatErrored("test:jid1") == false
```

Validates: error handling, cursor rollback, errored flag, recovery on next message.

### TC-06: Concurrent container limit

```
Given: cfg.MaxContainers = 2
       two groups: jid1→grp1, jid2→grp2, jid3→grp3
When:  send "delay:500" to all three simultaneously
Then:  two containers start immediately, third queues
       after ~500ms all three have replied
       total elapsed < 1100ms (not sequential)
```

Validates: `queue.GroupQueue` concurrency cap, no deadlock.

### TC-07: Container idle timeout

```
Given: cfg.IdleTimeout = 300ms, cfg.Timeout = 1s
When:  h.Send("delay:800")   // mock sleeps 800ms, longer than IdleTimeout
Then:  container killed at ~Timeout boundary
       Output.Error contains "timed out"
       store cursor rolled back (no output was delivered before kill)
```

Note: with `delay:` the mock sleeps before writing output. The runner's idle
timer fires before the mock completes. Use `cfg.Timeout = 500ms` to keep tests fast.

### TC-08: Heartbeat resets idle timer

```
Given: cfg.IdleTimeout = 200ms, cfg.Timeout = 2s
When:  h.Send("heartbeat:100")  // mock emits heartbeat every 100ms for 400ms then echoes ok
Then:  FakeChannel receives "ok" within 1s
       container was NOT killed by idle timer
```

Validates: heartbeat-in-output resets `timer.Reset(cfg.IdleTimeout)`.

### TC-09: MCP send_message roundtrip

```
Given: group testgrp with grants ["send_message(*)", "send_reply(*)"]
When:  h.Send("mcp:send_message")
Then:  mock calls MCP send_message tool with chatJid="test:jid1" text="mcp-test"
       FakeChannel receives "mcp-test" (via channel.Send called by GatedFns.SendMessage)
       mock emits success output
       FakeChannel receives the success echo too
```

Validates: MCP socket starts before container, unix socket reachable from inside
mock via socat, GatedFns wiring.

### TC-10: send_file MCP call

```
Given: grants include "send_file(*)"
When:  h.Send("file:tmp/report.txt")
Then:  mock writes "hello" to /home/node/tmp/report.txt
       mock calls MCP send_file with filepath="/home/node/tmp/report.txt"
       FakeChannel.SendFile called with path containing "tmp/report.txt"
```

Validates: `send_file` MCP handler + path translation in `workspaceRel`.

### TC-11: Multi-group routing

```
Given: two groups: jid1→grp1, jid2→grp2
When:  send "echo:for-group-1" to jid1
       send "echo:for-group-2" to jid2
Then:  FakeChannel.sent[0].JID == "test:jid1", text contains "for-group-1"
       FakeChannel.sent[1].JID == "test:jid2", text contains "for-group-2"
       (no cross-routing)
```

Validates: `groupForJid`, per-JID agent cursor isolation.

### TC-12: Scheduler message injection

```
Given: group registered
When:  store.PutMessage with Sender="scheduler-isolated:task-001"
       Content="echo:from scheduler"
Then:  FakeChannel receives "from scheduler" within 2s
       container.Input received by mock has isolated=true (derived from sender prefix)
```

Validates: scheduler sender prefix recognized, message picked up by poll loop.

### TC-13: Reply threading

```
Given: group registered
When:  h.Send("echo:first")
       wait for reply msg1 from FakeChannel (has ID "sent-001")
       h.SendWithReplyTo("echo:second", "sent-001")
Then:  mock receives prompt containing replyTo context
       FakeChannel.Send called with replyTo="sent-001"
```

Validates: `makeOutputCallback` → `lastSentID` → `sendMessageReply(replyTo)`.

---

## Implementation Plan

### Phase 1: mock agent binary

Location: `/home/onvos/app/arizuko/tests/mockagent/main.go`

~150 LOC Go binary:

1. Read stdin, JSON-unmarshal into `container.Input`-compatible struct.
2. Parse last line of `prompt` for command prefix.
3. For `mcp:` commands: open unix socket at `/workspace/ipc/gated.sock`,
   send JSON-RPC MCP `tools/call` request, read response.
4. Write output to stdout with markers.
5. Exit 0 (or 1 for `error:` commands).

### Phase 2: test harness

Location: `/home/onvos/app/arizuko/tests/e2e/harness.go`

Key problems to solve:

- `container.Run` calls `container.EnsureRunning()` which checks docker.
  E2e tests require docker to be running (this is fine for CI with docker-in-docker).
- `BuildMounts` writes to `groupDir` and calls `chown(groupDir, 1000, 1000)`.
  Tests run as root in CI or as developer uid. Use `ARIZUKO_DEV=0`.
- `seedSettings` writes `settings.json` — this happens regardless of mock; fine.
- `HostAppDir` must point to the real repo root so `MIGRATION_VERSION` check works.
  Use `repoRoot()` helper that walks `../..` from test file path.

### Phase 3: test cases

Location: `/home/onvos/app/arizuko/tests/e2e/e2e_test.go`

Start with TC-01 and TC-05 (happy path + error recovery). These catch the most
production failures. Add others in order of risk.

### Makefile target

```makefile
.PHONY: e2e e2e-image

e2e-image:
    CGO_ENABLED=0 GOOS=linux go build \
        -o tests/mockagent/mockagent ./tests/mockagent
    docker build -t arizuko-mock-agent:test tests/mockagent/

e2e: e2e-image
    go test -tags e2e -v -timeout 120s ./tests/e2e/... \
        2>&1 | tee ./tmp/e2e.log
```

---

## Gap Analysis: Why Unit Tests Pass But Production Fails

### What the existing tests mock out

`gateway_test.go` never calls:

- `container.Run()` — the entire docker lifecycle is absent
- `queue.GroupQueue.EnqueueMessageCheck` → `processGroupMessages` → `processSenderBatch`
- `store.NewMessages` in a real poll loop (only `loadState`, `saveState`, cursor methods)
- `ipc.ServeMCP` — the MCP socket is never started in any test

The `mockChannel` always returns `("", nil)` from `Send` — no real delivery
semantics. `SendFile` is a no-op.

### Production failure modes not caught

**1. Missing session file → `error_during_execution` → silent failure**

The SDK returns `subtype=error_during_execution` when the session transcript file
is missing or corrupted. The ant handles this by clearing `sessionId` and retrying.
The gateway sees `Output{Error: "no parseable output"}` (the ant exited 0 but never
wrote markers). Gateway then calls `store.DeleteSession` and sets cursor back.

Unit tests never exercise this because they never call `container.Run()`. TC-04
catches exactly this.

**2. Output marker parsing failure**

If the docker image doesn't exist or the binary crashes before writing markers,
`container.Run()` returns `Output{Error: "no parseable output"}`. Unit tests pass
because `processGroupMessages` is never called end-to-end. TC-04 and TC-07 catch
variations of this.

**3. Cursor rollback inconsistency**

`processSenderBatch` rolls back the cursor to `agentTs` (pre-run) on error with
no output. Unit tests only test `advanceAgentCursor` in isolation. TC-05 validates
the full rollback + retry flow.

**4. Concurrent container limit**

`queue.GroupQueue` is initialized with `MaxContainers` but unit tests create a
gateway with `MaxContainers=2` and never send concurrent messages. TC-06 is the
only test that exercises the queue under concurrency.

**5. MCP socket timing**

`ipc.ServeMCP` starts the unix socket before `docker run`. If the socket is not
ready when the container starts (race on slow hosts), the agent's first MCP call
fails. TC-09 is the first test that exercises socket readiness under real timing.

**6. Idle timer reset via heartbeat**

`container.Run()` calls `timer.Reset(cfg.IdleTimeout)` on each output marker
including heartbeats. Unit tests never call `Run()`. TC-08 is the only test that
validates this path.
