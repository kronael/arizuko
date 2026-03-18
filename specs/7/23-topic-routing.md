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

For tiers 0-2, insert `@` and `#` routes alongside default:

```go
if tier <= 2 {
    s.AddRoute(jid, store.Route{Seq: -2, Type: "prefix", Match: "@", Target: folder})
    s.AddRoute(jid, store.Route{Seq: -1, Type: "prefix", Match: "#", Target: folder})
}
```

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
5. Run container with that session
6. Store returned session via `SetSession(folder, sessionId, topic)`

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
This is a known limitation — future improvement: per-message
resolution before batching.

## Not in scope

- Agent-created topics
- Topic ACLs
- Topic listing command
- Cross-group topic routing
- Pipeline/DAG routing between topics
