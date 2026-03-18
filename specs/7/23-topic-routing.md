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

## Service routes

Route targets without `/` are service names, not group folders.
The router looks up the service URL in the channels table and
HTTP POSTs the message to its `/send` endpoint — same HTTP
contract as channel adapters.

Examples inserted at group-registration time or by operator:

```
seq=-10  type=prefix  match=/approve  target=onbod
seq=-10  type=prefix  match=/reject   target=onbod
seq=-10  type=prefix  match=/status   target=dashd
```

Negative seq ensures service routes are evaluated before
user-defined routes. `/approve` and `/reject` never reach
gated's command handler — they are resolved and dispatched
here like any other route.

## Implementation changes

| File                | Change                                                     |
| ------------------- | ---------------------------------------------------------- |
| core/types.go       | Route.Type: add "prefix"                                   |
| router/router.go    | RouteMatches: add prefix case                              |
| router/router.go    | ResolvedRoute: add Match field                             |
| router/router.go    | resolve service target (no `/`) via channels table         |
| store/sessions.go   | GetSession/SetSession/DeleteSession: add topic             |
| store/migrations    | sessions: add topic, change PK                             |
| store/migrations    | messages: add topic column                                 |
| gateway/gateway.go  | Handle @ and # via resolved.Match; dispatch service routes |
| gateway/commands.go | /new and /stop: parse #topic                               |

## Not in scope

- Agent-created topics
- Topic ACLs
- Topic listing command
- Cross-group topic routing
