---
status: shipped
---

# MCP Tool Authorization (per tier)

Scope: **which MCP tools an agent in a given folder can call.** Tier
is derived from folder depth; actions are scoped to tier 0-3.

For the broader auth picture — how `groups`, `user_groups`, `routes`
compose to produce the grant rules — see
[`specs/7/36-auth-landscape.md`](../7/36-auth-landscape.md).

**Path is identity, depth determines default grants.** Group identity
is the folder path; segment names are advisory. Tier is computed from
depth and decides which tool slots open. Core enforcement shipped.
Escalation response wiring shipped: `LocalChannel.Send` now enqueues a
message check on `local:<child>` and stores the parent reply as a
non-bot message so the child resumes and replies to the original user
JID.

## Tiers

Tier = `min(folder.split('/').length, 3)`. `root` is tier 0.

| Tier | Depth | Example             |
| ---- | ----- | ------------------- |
| 0    | 0     | `root`              |
| 1    | 1     | `atlas`             |
| 2    | 2     | `atlas/support`     |
| 3+   | 3+    | `atlas/support/web` |

Suggested human labels per depth (`world / org / branch / unit / thread`)
are documented in `ant/CLAUDE.md`. The system reads paths, not labels.

## Action authorization

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

> Updated 2026-04-24: tier 3+ can send_file (and send_reply), but not
> send_message. See `grants/grants.go:166` and commit `db288f4`
> ([feat] grants: tier 3+ can send files).

## Mount enforcement

| Mount                | Tier 0 | Tier 1 | Tier 2      | Tier 3      |
| -------------------- | ------ | ------ | ----------- | ----------- |
| `/home/node`         | rw     | rw     | rw          | ro          |
| `~/.claude/skills`   | --     | --     | ro overlay  | ro (parent) |
| `~/.claude/projects` | --     | --     | rw (parent) | rw overlay  |
| `/workspace/share`   | rw     | rw     | ro          | ro          |
| `/workspace/ipc`     | rw     | rw     | rw          | rw          |
| `/workspace/web`     | rw     | rw     | no          | no          |
| `/workspace/self`    | ro     | no     | no          | no          |
| `~/groups`           | rw     | no     | no          | no          |
| `/app/src`           | rw     | rw     | rw          | ro          |

## Delegation prompt format

```xml
<delegated_by group="atlas">
  ...original prompt...
</delegated_by>
```

Child knows via `ARIZUKO_DELEGATE_DEPTH > 0` env. Fire-and-forget;
child replies directly to `chatJid`.

## `local:` routing enforcement

All `local:` rules enforced in **action handlers** (not router).

- Downward: sender must be ancestor of target folder.
- Upward: `escalate_group` only, direct parent only (one level).
- `send_message` cannot target `local:` JIDs.

## Open: escalation response protocol

Currently fire-and-forget. Intended design:

```
user -> worker (chatJid = user_jid)
  worker calls escalate_group(prompt)
    -> parent runs with chatJid = local:worker_folder
      -> parent replies -> routed to worker
        -> worker replies to user_jid with replyTo: original_msg_id
```

Every registered group gets `local:{folder}` JID for internal routing.

Escalation XML:

```xml
<escalation from="atlas/support" reply_to="telegram:xxx" reply_id="789">
  <original_message sender="John" id="789">user text (max 200 chars)</original_message>
  ...worker's prompt...
</escalation>
```

reply_id by channel:

| Channel  | Type             | Send implementation                |
| -------- | ---------------- | ---------------------------------- |
| Telegram | integer string   | `reply_parameters: { message_id }` |
| Discord  | snowflake string | `message.reply()` — not yet        |
| WhatsApp | stanza ID        | needs quoted object — deferred     |
| Mastodon | status ID        | stub exists                        |
| Email    | Message-ID       | thread-based                       |

Circuit breaker: `MAX_DELEGATE_DEPTH = 1`. No recursive chaining.
