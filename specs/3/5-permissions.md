---
status: draft
---

## <!-- trimmed 2026-03-15: TS removed, shipped items collapsed, rich facts only -->

## status: partial

# Group Permissions

Four-tier permission model. Core enforcement shipped. Escalation
response protocol is the main open item.

## Tiers

- **Tier 0**: root (instance admin, folder = `root`)
- **Tier 1**: world (top-level folder)
- **Tier 2**: agent (depth 2)
- **Tier 3**: worker (depth 3+, clamped)

Tier derived from folder depth: `min(folder.split('/').length, 3)`.

## Action Authorization Summary

| Action         | Tier 0     | Tier 1       | Tier 2      | Tier 3  |
| -------------- | ---------- | ------------ | ----------- | ------- |
| send_message   | any target | same world   | own JID     | own JID |
| send_file      | any target | same world   | own JID     | denied  |
| schedule_task  | any target | same world   | own group   | denied  |
| register_group | children   | own world    | denied      | denied  |
| set_routing    | any group  | own children | denied      | denied  |
| delegate_group | any desc.  | own subtree  | own subtree | denied  |
| escalate_group | denied     | denied       | parent      | parent  |
| refresh_groups | allowed    | denied       | denied      | denied  |

## Mount Enforcement

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

## Delegation Prompt Format

```xml
<delegated_by group="atlas">
  ...original prompt...
</delegated_by>
```

Child knows via `ARIZUKO_DELEGATE_DEPTH > 0` env var. Fire-and-forget;
child replies directly to `chatJid`.

## local: Routing Enforcement

All `local:` rules enforced in **action handlers** (not router).

- Downward: sender must be ancestor of target folder
- Upward: `escalate_group` only, direct parent only (one level)
- `send_message` cannot target `local:` JIDs at all

## Open: Escalation Response Protocol

Currently fire-and-forget. Intended design:

```
user -> worker (chatJid = user_jid)
  worker calls escalate_group(prompt)
    -> parent runs with chatJid = local:worker_folder
      -> parent replies -> routed to worker as new message
        -> worker replies to user_jid with replyTo: original_msg_id
```

**`local:` JID namespace**: every registered group gets `local:{folder}`.
Created at registration, internal routing only.

**Escalation XML wrapper**:

```xml
<escalation from="atlas/support" reply_to="telegram:xxx" reply_id="789">
  <original_message sender="John" id="789">user text (max 200 chars)</original_message>
  ...worker's prompt...
</escalation>
```

**reply_id by channel**:

| Channel  | Type             | Send implementation                     |
| -------- | ---------------- | --------------------------------------- |
| Telegram | integer string   | `reply_parameters: { message_id }`      |
| Discord  | snowflake string | `message.reply()` -- not yet            |
| WhatsApp | stanza ID        | needs quoted message object -- deferred |
| Mastodon | status ID        | stub exists                             |
| Email    | Message-ID       | thread-based                            |

**Circuit breaker**: `MAX_DELEGATE_DEPTH = 1`. No recursive chaining.
