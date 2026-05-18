---
status: spec
depends: [C-message-mcp, G-engagement, T-voice-synthesis]
relates-to: [5/5-uniform-mcp-rest]
---

# specs/5/Z — message actions: edit, delete, pin, unpin

## Problem

Agents can send messages but cannot touch them afterward. Three gaps:

1. **Correction flow.** Agent posts a wrong number; the only recourse is a
   follow-up disclaimer. Edit-in-place is cleaner and reduces noise.

2. **Cleanup.** Agents that scaffold temporary scaffolding messages (task
   started, uploading…) cannot remove them. Delete lets them clean up.

3. **Status board.** A team wants one pinned message in a Slack channel that
   shows live deployment status. Today the agent spams a new message on each
   state change. Pin + edit-in-place gives a single live surface without
   thread noise.

`edit` and `delete` already exist as MCP tools backed by `chanlib.BotHandler`
verbs. They are undocumented and unguarded by capability checks. Pin/unpin
don't exist at all.

The agent already receives message IDs for its own sent messages in the
conversation XML context — no new tool needed to retrieve them. For the
status-board pattern, the agent writes the ID to its workspace file once and
reads it back on subsequent turns.

## The primitive

### MCP tools

| Tool            | Args                  | Returns | Tier | Notes                                 |
| --------------- | --------------------- | ------- | ---- | ------------------------------------- |
| `pin_message`   | `chatJid`, `targetId` | —       | 0–2  | Pin a message by platform ID          |
| `unpin_message` | `chatJid`, `targetId` | —       | 0–2  | Unpin a specific message              |
| `unpin_all`     | `chatJid`             | —       | 0–2  | Clear all pins in a chat (Slack only) |

`edit` and `delete` already exist in `ipc/ipc.go` — they need capability
guards (`chanreg` `HasCap("edit")` / `HasCap("delete")`) and clearer
descriptions that distinguish agent-authored-only scope from social
delete-any-message.

### BotHandler additions

`chanlib.BotHandler` gets two new verbs:

```
Pin(req PinRequest) error       // PinRequest{ChatJID, TargetID}
Unpin(req UnpinRequest) error   // UnpinRequest{ChatJID, TargetID, All bool}
```

Adapters that don't support pin embed `NoPinSupport` (returns
`chanlib.ErrUnsupported`), mapped to a `UnsupportedError` with a hint so the
agent can fall back gracefully.

`httpchan.HTTPChannel` in `chanreg/httpchan.go` proxies both verbs to
`POST /pin` and `POST /unpin` on the adapter, same pattern as existing verbs.

## Platform coverage

| Verb   | slakd                     | teled           | discd         | whapd                   | emaid |
| ------ | ------------------------- | --------------- | ------------- | ----------------------- | ----- |
| edit   | ✓ own msgs, no time limit | ✓ own msgs ≤48h | ✓ own msgs    | ✓ recent only (~15 min) | ✗     |
| delete | ✓ own                     | ✓ own           | ✓ own         | ✓                       | ✗     |
| pin    | ✓ channel pin             | ✓ channel pin   | ✓ channel pin | ✗                       | ✗     |
| unpin  | ✓                         | ✓               | ✓             | ✗                       | ✗     |

WhatsApp edit window is platform-enforced (~15 min); adapters surface this as
`ErrUnsupported` with `Hint: "message too old"` after the window closes.
Email has no mutable-message primitive; `edit` and `delete` always return
`ErrUnsupported`.

## Status-board pattern

The status-board is not a new primitive — it's a composition of existing ones:

1. Agent calls `send(chatJid, "Deploying v1.2.3…")` — the sent message ID
   appears in the next turn's XML context. Agent writes it to a workspace
   file for durable cross-turn recall.
2. On next state change, agent calls `edit(chatJid, <stored_id>, "Deploy complete ✓")`.
3. Agent calls `pin_message(chatJid, <stored_id>)` once, on first send.

The agent is responsible for persisting the message ID across turns (workspace
file). The platform holds the canonical live state.

## Code surface

| File                         | Change                                                                                                 | ~LOC     |
| ---------------------------- | ------------------------------------------------------------------------------------------------------ | -------- |
| `chanlib/handler.go`         | Add `PinRequest`, `UnpinRequest`, `Pin`, `Unpin` to `BotHandler`; add `NoPinSupport` mixin             | +30      |
| `chanreg/httpchan.go`        | Implement `Pin`, `Unpin` on `HTTPChannel`; add `HasCap("pin")` guard                                   | +25      |
| `ipc/ipc.go`                 | Add `pin_message`, `unpin_message`, `unpin_all` tools; add `HasCap` guards to existing `edit`/`delete` | +50      |
| `ipc/gated.go` (or inline)   | Wire `Pin`/`Unpin` into `GatedFns` struct                                                              | +10      |
| `slakd/`, `teled/`, `discd/` | Implement `Pin`/`Unpin` HTTP handlers (`POST /pin`, `POST /unpin`)                                     | +30 each |
| `whapd/`, `emaid/`           | Embed `NoPinSupport`; `emaid` embeds `NoEditDelete`                                                    | +5 each  |
| `ant/skills/self/`           | Migration + MIGRATION_VERSION bump; document new tools                                                 | +15      |

Total: ~200 LOC across adapters + plumbing.

## What this is NOT

- **Message history search.** `get_history` / `get_thread` already cover
  retrieval. This spec adds mutation, not lookup.
- **Bulk moderation.** No "delete all messages from user X". Single-message
  scope only.
- **Reaction management.** `like` / `dislike` are separate verbs, already
  shipped. This spec doesn't touch them.
- **Scheduled edits.** `timed` handles scheduling. An agent that wants a
  timed edit schedules a task; no new primitive needed.
- **Cross-platform message ID mapping.** IDs are opaque platform strings;
  the adapter owns the mapping. No normalization layer.
