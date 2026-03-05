# Experiment 1 — SDK stale session ID behavior

**Question:** When the agent container receives a session ID the SDK doesn't
recognize, does it throw, silently start fresh, or return an error?

## Plan

Run the container by hand with a fabricated bad session ID on stdin. No code
changes needed.

```bash
# 1. Find a real session folder to mount (any kanipi instance data dir)
GROUPS_DIR=/srv/data/REDACTED/groups
SESSION_DIR=/srv/data/REDACTED/data/sessions/main

# 2. Fabricate a bad session ID (valid UUID format, no matching .jl file)
BAD_SESSION="00000000-0000-0000-0000-000000000000"

# 3. Build the stdin payload — same format as container-runner.ts ContainerInput
PAYLOAD=$(cat <<EOF
{
  "prompt": "say hello",
  "sessionId": "$BAD_SESSION",
  "groupFolder": "main",
  "chatJid": "tg:-1",
  "sender": "test",
  "timestamp": "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
}
EOF
)

# 4. Run the container exactly as the gateway would
echo "$PAYLOAD" | docker run -i --rm \
  -v "$GROUPS_DIR/main:/workspace/group/main" \
  -v "$SESSION_DIR/.claude:/home/node/.claude" \
  kanipi-agent:latest
```

## What to observe

- Does it exit with non-zero? Check `$?`
- Does stdout contain `{ status: 'error', ... }` or `{ status: 'success', newSessionId: ... }`?
- Is `newSessionId` in the output a fresh UUID (silent new session) or absent (error)?
- Check `$SESSION_DIR/.claude/debug/` for any SDK error log after the run
- Check if a new `.jl` file appears under `$SESSION_DIR/.claude/projects/`

## Expected outcomes

| Outcome                          | Meaning                     | Gateway action needed                                 |
| -------------------------------- | --------------------------- | ----------------------------------------------------- |
| `status: success`, new UUID      | SDK silently starts fresh   | Always store `newSessionId` — no detection needed     |
| `status: error`                  | SDK throws on bad resume    | Catch error, clear stored ID, retry without sessionId |
| `status: success`, same bad UUID | SDK ignores resume silently | Need to detect stale ID another way                   |

## Records to update

`specs/v1/memory-session.md` open item 1. Delete this file when done.
