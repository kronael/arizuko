# Experiment 1 — SDK stale session ID behavior — DONE

## Result

Ran on REDACTED with `kanipi-agent:latest`, bad session
`00000000-0000-0000-0000-000000000000`:

```
[agent-runner] Starting query (session: 00000000-..., resumeAt: latest)...
[agent-runner] [msg #1] type=result subtype=error_during_execution
{"status":"success","result":null}
[agent-runner] Session initialized: fa649547-7d60-4ee9-b579-f09be6a7d0fe
[agent-runner] Result #2: subtype=success text=Not logged in · Please run /login
{"status":"success","result":"Not logged in · Please run /login","newSessionId":"fa649547-..."}
[agent-runner] Agent error: Claude Code process exited with code 1
{"status":"error","result":null,"newSessionId":"00000000-...","error":"Claude Code process exited with code 1"}
exit: 1
```

## Findings

1. SDK silently recovers: `error_during_execution` on bad resume → new session
   started automatically (`fa649547...`)
2. Agent-runner exits with code 1 and emits `status: error` with the
   **original bad ID** as `newSessionId` — the new session UUID is lost
3. Gateway currently does not handle `status: error` — stored session ID
   stays stale forever

## Fix needed

In `container-runner.ts`: on `status: error`, clear stored session ID.
Optionally capture `newSessionId` from the error output if it differs from
what was passed in. See `specs/v1/memory-session.md` open item 1.

Delete this file once the fix is shipped.
