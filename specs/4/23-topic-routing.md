---
status: shipped
---

# Topic Routing

Inline routing symbols `@agent` and `#topic` in message content.
Handled entirely in the gateway's prefix layer — no routes table
rows are involved. The prefix layer runs after the command layer
and before the routing layer.

## Routing symbols

### @agent — route to subgroup

`@support hello` routes to `<group.Folder>/support` (child group).
Prefix stripped before agent sees the message.

- Parsed inline from `msg.Content`
- Resolves target: `<parent>/<name>` where `<parent>` is the
  folder of the group that owns the incoming room
- Child must exist in the groups table
- Message delivered via `delegateViaMessage` with the stripped text
- If child doesn't exist: log and drop

### #topic — route to named session

`#deploy let's review` routes to session "deploy" within the
same group. Same agent, same folder, different session.

- Parsed inline from `msg.Content`
- Target: same group folder (self-route with topic context)
- Creates or resumes a named session keyed by `(group_folder, topic)`
- Agent sees only messages from that topic's session history
- `#` prefix consumed — agent sees "let's review"
- No prefix = default session (topic = "")

### Difference

|              | @agent                    | #topic                        |
| ------------ | ------------------------- | ----------------------------- |
| Routes to    | different group/container | same group, different session |
| Agent config | can differ                | same                          |
| Folder       | different                 | same                          |
| Context      | separate                  | separate                      |

## No routes table rows

Prefix handling is entirely in code. The routes table never stores
`@` or `#` rows. When a group is registered, gateway inserts one row:

```sql
INSERT OR IGNORE INTO routes (seq, match, target)
VALUES (0, 'room=<post-colon of jid>', '<folder>');
```

The prefix layer reads `msg.Content`, detects an inline `@name` or
`#name` token at the start, and dispatches accordingly. The
message's owning group is resolved first (via the routing layer's
`groupForJid`), so the prefix layer always has a base folder to
navigate from.

## Pipeline order

```
1. sticky layer   — bare @name / #topic updates routing state
2. command layer  — /new, /ping, /approve etc. from gatewayCommands table
3. prefix layer   — inline @name / #name navigation (this doc)
4. routing layer  — walks routes table, first match wins
```

Commands and prefixes never consult the routes table. Only the
routing layer reads it.

## @agent resolution

1. Parse `@<name>` from `msg.Content`
2. Resolve child folder: `<group.Folder>/<name>`
3. Check child exists in groups table
4. Strip prefix, delegate via `delegateViaMessage`

## #topic resolution

1. Parse `#<name>` from `msg.Content`
2. Target is self (same folder)
3. Strip prefix
4. Look up `GetSession(folder, topic)` for topic-specific session
5. Run container with that session and topic in RunConfig
6. Store returned session via `SetSession(folder, topic, sessionId)`

## Store function signatures

Existing `GetSession`/`SetSession` in `store/sessions.go` take only
`folder string`. Change signatures to add `topic`:

```go
// GetSession returns (sessionID, true) or ("", false) if not found.
func (s *Store) GetSession(folder, topic string) (string, bool)

// SetSession stores session ID for (folder, topic).
func (s *Store) SetSession(folder, topic, sessionID string) error
```

Existing call sites update to pass `topic = ""`.

## RunConfig topic field

```go
type RunConfig struct {
    // ... existing fields ...
    Topic string // "" for default session; "#name" for named topic
}
```

`container.Run` passes `Topic` into `start.json` as `"topic"` field
and appends `"Topic session: #name"` to `annotations` when non-empty.

## delegateViaMessage

`delegateViaMessage` is an existing function in `gateway/gateway.go`.
For `@agent` dispatch: `folder=childFolder`, `prompt=stripped text`,
`originJid=msg.ChatJID`, `depth=0`.

The child folder is `group.Folder + "/" + name` where `name` is
parsed from `@name` and `group` is the owning group returned by
`groupForJid` for the current message.

## start.json topic injection

```json
{ "topic": "deploy", "annotations": ["Topic session: #deploy"] }
```

## Command integration

- `/new #topic msg` — reset named topic session: `DELETE FROM sessions WHERE group_folder=? AND topic=?`, then route msg to that topic
- `/stop #topic` — not in scope; standard `/stop` stops all containers for the folder
- No prefix = default session (topic="")

## Batch ordering note

Prefix resolution happens on the last non-command message in a poll
batch. If multiple messages arrive together with different `@`/`#`
prefixes, the last message determines dispatch for the entire batch.
All messages in the batch (including earlier ones with different
prefixes) are delivered to the target resolved from the last message.
Earlier prefix content is not lost — it arrives in the container's
message context — but routing is determined by the final message.
This is a known limitation — future improvement: per-message
resolution before batching.

## Not in scope

- Agent-created topics
- Topic ACLs
- Topic listing command
- Cross-group topic routing
- Pipeline/DAG routing between topics
