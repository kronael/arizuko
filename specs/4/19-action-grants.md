---
status: superseded-in-part
superseded-in-part: [5/32-acl-unified, 5/33-paths-roles]
---

# Action grants — the rule grammar

> **Storage and derivation are gone.** The `grant_rules` table and the
> `**` marker on `user_groups` collapsed into `acl`
> ([`../5/32-acl-unified.md`](../5/32-acl-unified.md)); rule overrides
> became `acl` rows with `action='mcp:<tool>'`. The whole `grants/`
> package — `DeriveRules`, `CheckAction`, `MatchingRules`,
> `platformActions` — was **deleted**: `mcp:*` defaults are no longer
> derived from tier but seeded as explicit role grants and delegated via
> `grant_option` ([`../5/33-paths-roles.md`](../5/33-paths-roles.md)).
> Two things survive, and they are the reason this file still exists.

## 1. The rule grammar

Still the per-row policy shape:

```
[!]action_glob[(param=glob, ...)]
```

```
send                    allow send, any params
send(jid=telegram:*)    allow send, only telegram targets
!send                   deny send
send_file(!jid)         allow send_file, jid must NOT be present
*                       allow everything
```

- `!` prefix = deny.
- `*` in an action name matches `[a-zA-Z0-9_]` only — so `send*` cannot
  accidentally reach across a namespace separator.
- `*` in a param value matches any character except `,` and `)`.
- Unmentioned params are allowed; `!param` means the param must be
  absent; no parens is equivalent to `()`, meaning any params.
- **Last match wins, and no match is deny.** Default-deny is what makes
  a truncated or partially-applied rule set fail closed.

Action names are MCP tool names. Platform scoping rides the `jid` param
rather than a separate platform field, so one grammar covers "which
tool" and "against what" without a second dimension.

## 2. Structured unsupported errors

When an adapter has no native primitive for a verb it returns
`*chanlib.UnsupportedError` carrying `{Tool, Platform, Hint}` — a 501
with a body, not a bare failure:

```json
{
  "ok": false,
  "error": "unsupported",
  "tool": "quote",
  "platform": "mastodon",
  "hint": "..."
}
```

`chanreg.HTTPChannel` decodes it into a typed value and ipc renders both
lines (`unsupported: quote on mastodon` + `hint: ...`).
`errors.Is(err, chanlib.ErrUnsupported)` chains, so call sites that only
care about the class keep working.

This matters because "not supported here" and "failed" need different
agent responses: one calls for a different verb, the other for a retry.
Collapsing them into a generic error made agents retry verbs that could
never succeed.

## Explicitly not carried forward

Grant expiry/TTL, rule inheritance across worlds, and per-call grant
narrowing on `delegate_group` (prototyped as `NarrowRules`, removed).
Delegation narrowing is now the `grant_option` level in `5/33`.
