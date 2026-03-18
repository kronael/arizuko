# Action Grants

**Status**: design

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
*                                allow everything (root default)
```

- `!` prefix = deny
- `*` in name matches `[a-zA-Z0-9_]` only
- `*` in param values matches any char except `,` and `)`
- Unmentioned params = allowed
- `!param` = param must NOT be present
- No parens or `()` = any params
- Last match wins; no match = deny

Action names match MCP tool names: `send_message`, `send_reply`,
`send_file`, `schedule_task`, `delegate_group`, etc.
Platform scoping via `jid` param (e.g. `jid=twitter:*`).

## Defaults (from routing table)

Derived from routing + tier. Platform access determined by
which JIDs have routes to the group.

- **Tier 0 (root)** — `["*"]`. All actions, all params.
- **Tier 1 (world root)** — all actions on every platform with
  at least one route anywhere in the world.
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

Child rules = parent rules + narrowing rules appended.
Can only narrow, never widen.

## Module: `grants/`

Self-contained package. No dependency on ipc or gateway.

```go
func ParseRule(r string) Rule
func DeriveRules(s *store.Store, folder string, tier int) []string
func CheckAction(rules []string, action string, params map[string]string) bool
func MatchingRules(rules []string, action string) []string
func NarrowRules(parent, child []string) []string
```

`DeriveRules` reads routes from DB to determine which platforms
are accessible per tier, generates allow rules per action+platform.

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

## Not in scope

- Grant expiry / TTL
- Rule inheritance across worlds
