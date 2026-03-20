# Action Grants

**Status**: shipped

Grant rules control which MCP actions a container can call.
Rules derived from routing + tier at spawn, injected into
`start.json`, validated at dispatch. Agents see allowed actions
with matching rules in the manifest.

## Rule syntax

```
[!]action_glob[(param=glob, ...)]
```

```
send_message                     allow send_message, any params
send_message(jid=telegram:*)     allow send_message, only telegram
!send_message                    deny send_message
send_reply                       allow reply in same thread
send_file(!jid)                  allow send_file, jid must NOT be present
*                                allow everything (root default)
```

- `!` prefix = deny
- `*` in name matches `[a-zA-Z0-9_]` only
- `*` in param values matches any char except `,` and `)`
- Unmentioned params = allowed
- `!param` inside parens = param must NOT be present
- No parens or `()` = any params (equivalent)
- Last match wins; no match = deny

Action names match MCP tool names: `send_message`, `send_reply`,
`send_file`, `schedule_task`, `delegate_group`, etc.
Platform scoping via `jid` param (e.g. `jid=telegram:*`, `jid=discord:*`).

## Defaults (from routing table)

Derived from routing + tier. Platform access determined by
which JIDs have routes to the group.

- **Tier 0 (root)** — `["*"]`. All actions, all params.
- **Tier 1 (world root)** — all actions on every platform with
  at least one route whose target folder is a descendant of
  the tier-1 group's world root (i.e., `target = world OR
target LIKE world || '/%'` in the routes table).
- **Tier 2** — `send_message`, `send_reply`, plus actions on
  platforms routed to self or children.
- **Tier 3+ (leaf)** — `send_reply` only. Same chat/thread.

## Overrides (DB)

```sql
CREATE TABLE grants (
  folder TEXT NOT NULL PRIMARY KEY,
  rules  TEXT NOT NULL  -- JSON string[]
);
```

Override rules appended after defaults. Last-match-wins.
No row = defaults only.

Note: actual DB schema uses `(id, jid, role, granted_by, granted_at)` for
audit trail — not `(folder, rules TEXT JSON)` as originally specced above.

## Token in start.json

```json
{ "grants": ["send_reply", "!send_message"] }
```

## Agent manifest

Denied actions omitted. Allowed actions include matching rules:

```json
{ "name": "send_reply", "grants": ["send_reply"] }
```

## Delegation

`NarrowRules(parent, child []string) []string` appends child rules
to parent, then strips any child allow rule where
`CheckAction(parent, action, nil)` is false — i.e. an allow rule
that the parent does not permit is silently dropped. Child deny
rules always pass through unchanged. Result: delegation can only
restrict, never expand.

`delegate_group` `grants` param: JSON string array, e.g.
`["send_reply","!send_file"]`. Empty or absent = no narrowing.

## Module: `grants/`

Self-contained package. No dependency on ipc or gateway.

```go
// Rule and ParseRule already exist in grants/grants.go:
type Rule struct {
    Deny   bool
    Action string
    Params map[string]ParamRule
}
type ParamRule struct { Deny bool; Pattern string }
func ParseRule(r string) Rule  // already implemented

// Functions to add:
func DeriveRules(s *store.Store, folder string, tier int, worldFolder string) []string
func CheckAction(rules []string, action string, params map[string]string) bool
func MatchingRules(rules []string, action string) []string
func NarrowRules(parent, child []string) []string
```

- `DeriveRules`: `worldFolder` = folder itself for tier 1 (tier-1 group IS the world root); for tier 2+, caller derives worldFolder by walking parent chain.
- `CheckAction([]string{}, action, nil)` → `false` (no rules = deny)
- `NarrowRules(parent, nil)` or `NarrowRules(parent, []string{})` → returns `parent` unchanged

### DeriveRules output

Platforms = JID prefixes (e.g. `telegram`, `discord`) extracted
from route source JIDs in scope.

- **Tier 0**: `["*"]`
- **Tier 1**: always `["schedule_task", "delegate_group",
"register_group", "escalate_group", "get_routes", "set_routes",
"add_route", "delete_route", "list_tasks", "pause_task",
"resume_task", "cancel_task"]` plus, for each platform P in
  world: `["send_message(jid=P:*)", "send_reply(jid=P:*)",
"send_file(jid=P:*)"]`
- **Tier 2**: for each platform P routed to self or children:
  `["send_message(jid=P:*)", "send_reply(jid=P:*)"]`
- **Tier 3+**: `["send_reply"]`

DB override rules are appended after defaults.

### MatchingRules

Returns all rules (allow and deny) whose action glob matches
`action`. The caller is responsible for filtering: if
`CheckAction(result, action, nil)` is false, omit the tool from
the manifest. MatchingRules does not filter deny rules itself.
`nil` params and empty-map params are equivalent in `CheckAction` —
both mean "no params provided".

### Store query for DeriveRules

Add to `store/`:

```go
// RouteSourceJIDsInWorld returns distinct source JIDs whose route
// target is the worldFolder or a descendant (target = worldFolder
// OR target LIKE worldFolder || '/%').
func (s *Store) RouteSourceJIDsInWorld(worldFolder string) []string
```

Platform prefix extracted as the part before `:` in each JID.

## Integration

- `container/runner.go`: call `DeriveRules`, add `grants` to start.json
- `ipc/ipc.go`: call `CheckAction` before tool execution,
  deny with error if check fails. Replaces `auth.Authorize`
- `ipc/ipc.go`: call `MatchingRules` per tool for manifest
- `gateway/gateway.go`: `delegate_group` passes optional `grants`
  param, calls `NarrowRules`

## MCP actions

- `set_grants(folder, rules)` — replace rules (tier 0-1 only)
- `get_grants(folder)` — list rules
- `delegate_group` gains optional `grants` param

## Authority

- Tier 0 — any grant
- Tier 1 — descendants in own world
- Tier 2+ — cannot modify grants

## Security

- Agent cannot edit grants DB (not in container)
- Rules ephemeral per-session (derived at spawn, passed in start.json)
- Delegation can only narrow (`NarrowRules`)
- No grants in start.json → `["*"]` (backward compat)

## Not in scope

- Grant expiry / TTL
- Rule inheritance across worlds
