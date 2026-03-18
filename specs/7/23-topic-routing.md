# Topic Routing

**Status**: design

Routing symbols `@agent` and `#topic` as predefined route
table entries. Created automatically on group registration
for tiers 0-2.

## Routing symbols

### @agent — route to subgroup

`@support hello` routes to `<parent>/support` (child group).
Prefix stripped before agent sees the message.

- Route type: `prefix`, match: `@`
- Resolves target: `<parent>/<name>`
- Child must exist in groups table
- Message delivered via `delegateToFolder` with stripped text
- If child doesn't exist: log and drop

### #topic — route to named session

`#deploy let's review` routes to session "deploy" within
the same group. Same agent, same folder, different session.

- Route type: `prefix`, match: `#`
- Target: same group folder (self-route with topic context)
- Creates or resumes named session keyed by `(group_folder, topic)`
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

## Predefined routes on group creation

For tiers 0-2, insert `@` and `#` routes for the registration JID:

```go
if tier <= 2 {
    s.AddRoute(jid, store.Route{Seq: -2, Type: "prefix", Match: "@", Target: folder})
    s.AddRoute(jid, store.Route{Seq: -1, Type: "prefix", Match: "#", Target: folder})
}
```

`jid` is the source JID passed to `register_group`. Routes are per
source-JID. When `add_route` is called in `ipc/ipc.go` and the
target group's tier is 0-2, the handler also inserts `@` and `#`
prefix routes for the same source JID (after inserting the
requested route):

```go
g, ok := db.GetGroupByFolder(route.Target)
if ok && g.Tier <= 2 {
    db.AddRoute(route.JID, store.Route{Seq: -2, Type: "prefix", Match: "@", Target: g.Folder})
    db.AddRoute(route.JID, store.Route{Seq: -1, Type: "prefix", Match: "#", Target: g.Folder})
}
```

`store.AddRoute` uses `INSERT OR IGNORE` semantics — duplicate
`(jid, seq, match)` entries are silently skipped.

Negative seq ensures `@` and `#` evaluated before user routes.

## Route matching

New route type `prefix` in `router.RouteMatches`:

```go
case "prefix":
    return strings.HasPrefix(strings.TrimSpace(msg.Content), r.Match)
```

After match, caller parses the full prefix:

```go
func ParsePrefix(text string) (name, rest string, ok bool)
```

## @agent resolution

1. Parse `@<name>` from message
2. Resolve child folder: `<target>/<name>`
3. Check child exists in groups table
4. Strip prefix, delegate via `delegateToFolder`

## #topic resolution

1. Parse `#<name>` from message
2. Target is self (same folder)
3. Strip prefix
4. Look up `GetSession(folder, topic)` for topic-specific session
5. Run container with that session and topic in RunConfig
6. Store returned session via `SetSession(folder, topic, sessionId)`

## Store function signatures

```go
// GetSession returns session ID for (folder, topic). topic="" = default.
func (s *Store) GetSession(folder, topic string) string

// SetSession stores session ID for (folder, topic).
func (s *Store) SetSession(folder, topic, sessionID string)
```

Existing call sites that pass only folder use `topic = ""`.

## RunConfig topic field

```go
type RunConfig struct {
    // ... existing fields ...
    Topic string // "" for default session; "#name" for named topic
}
```

`container.Run` passes `Topic` into `start.json` as `"topic"` field
and appends `"Topic session: #name"` to `annotations` when non-empty.

## Schema changes

Sessions table gains `topic` column, PK changes:

```sql
CREATE TABLE sessions_new (
  group_folder TEXT NOT NULL,
  topic TEXT NOT NULL DEFAULT '',
  session_id TEXT NOT NULL,
  PRIMARY KEY (group_folder, topic)
);
```

Messages table gains topic column:

```sql
ALTER TABLE messages ADD COLUMN topic TEXT DEFAULT '';
```

## start.json topic injection

```json
{ "topic": "deploy", "annotations": ["Topic session: #deploy"] }
```

## Command integration

- `/new #topic msg` — reset named topic session, route msg
- `/stop #topic` — stop container for that topic
- No prefix = default session

## Evaluation order

```
1. Gateway commands (/new, /stop, /ping, etc.)
2. Route table scan (seq-ordered, first match wins)
   seq -2: @ prefix (predefined for tiers 0-2)
   seq -1: # prefix (predefined for tiers 0-2)
   seq  0: default route
   seq  N: user-defined routes
```

## Batch ordering note

Route resolution happens on the last non-command message in a poll
batch. If multiple messages arrive together with different `@`/`#`
prefixes, the last message determines routing for the entire batch.
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
