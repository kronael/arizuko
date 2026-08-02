---
status: superseded-in-part
superseded-in-part: [5/33-paths-roles]
---

# MCP tool authorization per tier (the replaced model)

> **Tiers are dissolved.** `auth.Resolve` now returns a folder that
> "carries ZERO authorization — only its own name"
> (`auth/identity.go`); capability comes from `role` bindings + `acl`
> rows with a `grant_option` delegation level
> ([`../5/33-paths-roles.md`](../5/33-paths-roles.md)), never from folder
> depth. This file is kept for one reason: the tier×action table below
> is the **source** for the default-role bundles `5/33` seeds
> (`role:owner` ≈ old tier 1, `role:member` ≈ old tier 2). Do not add
> tier logic here.

Tier was `min(len(folder.split("/")), 3)`, with `root` at 0.

## Tier × action (source for the seeded role bundles)

| Action         | Tier 0     | Tier 1       | Tier 2      | Tier 3  |
| -------------- | ---------- | ------------ | ----------- | ------- |
| send_message   | any target | same world   | own JID     | denied  |
| send_file      | any target | same world   | own JID     | own JID |
| schedule_task  | any target | same world   | own group   | denied  |
| register_group | children   | own world    | denied      | denied  |
| set_routing    | any group  | own children | denied      | denied  |
| delegate_group | any desc.  | own subtree  | own subtree | denied  |
| escalate_group | denied     | denied       | parent      | parent  |
| refresh_groups | allowed    | denied       | denied      | denied  |

Tier 3+ gained `send_file` and `send_reply` (but never `send_message`)
so a leaf room could answer with an artifact without acquiring
broadcast authority.

## Tier × mount (source for the container-capability grants)

| Mount                | Tier 0 | Tier 1 | Tier 2      | Tier 3      |
| -------------------- | ------ | ------ | ----------- | ----------- |
| `/home/node`         | rw     | rw     | rw          | ro          |
| `~/.claude/skills`   | --     | --     | ro overlay  | ro (parent) |
| `~/.claude/projects` | --     | --     | rw (parent) | rw overlay  |
| `~/public_html`      | rw     | rw     | rw          | rw          |
| `~/private_html`     | rw     | rw     | rw          | rw          |
| `/var/lib/share`     | rw     | rw     | ro          | ro          |
| `/run/ipc`           | rw     | rw     | rw          | rw          |
| `/var/lib/www`       | rw     | ro     | ro          | no          |
| `/opt/arizuko`       | ro     | no     | no          | no          |
| `/var/lib/groups`    | rw     | no     | no          | no          |
| `/app/src`           | rw     | rw     | rw          | ro          |

These became explicit grants (`ShareReadOnly`, `EgressOpen`,
`WebPublish`), resolved by routd at dispatch — `routd/dispatch.go`.
`~/public_html` / `~/private_html` bind-mount per-group from the unified
web tree; [`../5/V-web-vhosts.md`](../5/V-web-vhosts.md) is canonical.

## Delegation, which outlived the tiers

Downward delegation requires the sender to be an ancestor of the target
folder; upward is `escalate_group` only, to the direct parent, one
level. `send_message` cannot target a `local:` JID. The circuit breaker
is `MAX_DELEGATE_DEPTH = 1` — no recursive chaining, because a fan-out
loop between two agents has no natural stopping point.

Delegated prompts arrive wrapped, and the child learns its depth from
`ARIZUKO_DELEGATE_DEPTH`:

```xml
<delegated_by group="atlas">...original prompt...</delegated_by>
```

Escalation replies route back through `local:<child>`: the parent's
answer is stored as a non-bot message so the child resumes and answers
the original user JID rather than the parent.
