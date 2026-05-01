---
status: shipped
---

# Action Grants

Grant rules control which MCP actions a container can call.
Rules derived from routing + tier at spawn, injected into
`start.json`, validated at dispatch. Agents see allowed actions
with matching rules in the manifest.

## Rule syntax

```
[!]action_glob[(param=glob, ...)]
```

```
send                     allow send, any params
send(jid=telegram:*)     allow send, only telegram
!send                    deny send
reply                       allow reply in same thread
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

Action names match MCP tool names: `send`, `reply`,
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
- **Tier 2** — `send`, `reply`, plus actions on
  platforms routed to self or children.
- **Tier 3+ (leaf)** — `reply`, `send_file`, `like`, `edit` only
  (see "Action lists" below). Same chat/thread.

## Overrides (DB)

```sql
CREATE TABLE grant_rules (
  folder TEXT NOT NULL PRIMARY KEY,
  rules  TEXT NOT NULL  -- JSON string[]
);
```

Override rules appended after defaults. Last-match-wins.
No row = defaults only.

Note: a separate `grants` table `(id, jid, role, granted_by, granted_at)`
exists for user/role grants and is unrelated to the action-rule overrides
described here.

## Token in start.json

```json
{ "grants": ["reply", "!send"] }
```

## Agent manifest

Denied actions omitted. Allowed actions include matching rules:

```json
{ "name": "reply", "grants": ["reply"] }
```

## Delegation

`delegate_group` does not narrow grants today; the child group runs with
its own derived rules. Per-call narrowing is unimplemented (the previous
`NarrowRules` helper was removed in commit c63ea8e); see "Not in scope".

## Module: `grants/`

Self-contained package. Depends only on `core` and `store`; no dependency
on ipc or gateway.

```go
type Rule struct {
    Deny   bool
    Action string
    Params map[string]ParamRule
}
type ParamRule struct { Deny bool; Pattern string }

func ParseRule(r string) Rule
func DeriveRules(s *store.Store, folder string, tier int, worldFolder string) []string
func CheckAction(rules []string, action string, params map[string]string) bool
func MatchingRules(rules []string, action string) []string
```

- `DeriveRules`: `worldFolder` = folder itself for tier 1 (tier-1 group IS the world root); for tier 2+, caller derives worldFolder by walking parent chain.
- `CheckAction([]string{}, action, nil)` → `false` (no rules = deny)

### DeriveRules output

Platforms = JID prefixes (e.g. `telegram`, `discord`) extracted
from route source JIDs in scope. The exact action lists per tier are
canonicalized in "Action lists (post-073)" below; this section gives
the shape.

- **Tier 0**: `["*"]`.
- **Tier 1**: ungated `send`, `send_file`, `reply`; per-platform
  `(jid=P:*)` rules over the routed-platform set in this world for every
  verb in `platformActions`; the fixed management list
  (`tier1FixedActions`); plus `share_mount(readonly=false)`.
- **Tier 2**: ungated `send`, `send_file`, `reply`; per-platform
  `(jid=P:*)` rules over the platforms routed to this folder; plus
  `share_mount(readonly=true)`.
- **Tier 3+**: `["reply", "send_file", "like", "edit"]` so leaf rooms
  can edit their own outputs without gaining broadcast/post authority.

DB override rules from `grant_rules` are appended after defaults.

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

- `gateway/gateway.go`: calls `DeriveRules` at spawn, adds `grants` to start.json
- `ipc/ipc.go`: calls `CheckAction` before tool execution; denies with
  error if check fails. Pairs with `auth.Authorize` for tier/scope checks
- `ipc/ipc.go`: calls `MatchingRules` per tool for the manifest

## MCP actions

- `set_grants(folder, rules)` — replace `grant_rules` row (tier 0-1 only)
- `get_grants(folder)` — read the row

## Authority

- Tier 0 — any grant
- Tier 1 — descendants in own world
- Tier 2+ — cannot modify grants

## Security

- Agent cannot edit grants DB (not in container)
- Rules ephemeral per-session (derived at spawn, passed in start.json)
- No grants in start.json → `["*"]` (backward compat)

## Not in scope

- Grant expiry / TTL
- Rule inheritance across worlds
- Per-call grant narrowing on `delegate_group` (was prototyped as
  `NarrowRules` and removed)

## Action lists (post-073)

Two action lists drive `DeriveRules` in `grants/grants.go`:

- `basicSendActions = {send, send_file, reply}` — emitted ungated at
  tier 1 and tier 2 so a routed group can address any peer on any of
  its platforms without per-jid rules.
- `platformActions = {send, send_file, reply, forward, post, quote,
repost, like, dislike, delete, edit}` — emitted as
  `verb(jid=<plat>:*)` for every platform in scope. Send verbs appear
  in both lists so the ungated form covers in-platform replies and the
  scoped form covers cross-platform addressing.

`platformRules` iterates `platformActions` over the routed-platform
set. Tier 0 = `*`. Tier 1 = world-scope routes + tier-1 management +
RW share_mount. Tier 2 = folder-scope routes + RO share_mount. Tier
3+ = `{reply, send_file, like, edit}` so leaf rooms can edit their
own outputs without gaining broadcast/post authority.

## Structured unsupported errors

When an adapter has no native primitive for a verb, it returns
`*chanlib.UnsupportedError` carrying `{Tool, Platform, Hint}`. The
HTTP wire format on 501 is:

```json
{
  "ok": false,
  "error": "unsupported",
  "tool": "quote",
  "platform": "mastodon",
  "hint": "..."
}
```

`chanreg.HTTPChannel` decodes the body into a typed value; ipc renders
it as a tool error with both lines: `unsupported: quote on mastodon\nhint: ...`.
`errors.Is(err, chanlib.ErrUnsupported)` chains so existing call sites
remain compatible.
