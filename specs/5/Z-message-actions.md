---
status: draft
depends: [C-message-mcp, G-engagement, T-voice-synthesis]
relates-to: [5/5-uniform-mcp-rest]
---

# specs/5/Z — message actions: edit, delete, pin, unpin

`edit` and `delete` already exist as MCP tools. `edit` is cap-guarded in
`chanreg/httpchan.go`; `delete` is not. Pin/unpin don't exist. Agents see
their own message IDs in the conversation XML context; no retrieval
primitive needed.

## MCP tools

| Tool            | Args                             | Returns | Tier | Notes                           |
| --------------- | -------------------------------- | ------- | ---- | ------------------------------- |
| `edit`          | `chatJid`, `targetId`, `content` | `ok`    | 0–2  | Existing. Cap-guarded.          |
| `delete`        | `chatJid`, `targetId`            | `ok`    | 0–2  | Existing. Add cap guard.        |
| `pin_message`   | `chatJid`, `targetId`            | `ok`    | 0–2  | New. Cap `pin`.                 |
| `unpin_message` | `chatJid`, `targetId`            | `ok`    | 0–2  | New. Cap `pin`.                 |
| `unpin_all`     | `chatJid`                        | `ok`    | 0–2  | New. Cap `pin`. Slack/Telegram. |

All four follow the existing `regSocial` pattern (grant check, authorize,
JID-owner check, slog, dispatch). On 501 the agent receives a structured
`UnsupportedError{tool, platform, hint}`.

## BotHandler additions (`chanlib/handler.go`)

```go
type PinRequest   struct { ChatJID, TargetID string }
type UnpinRequest struct { ChatJID, TargetID string; All bool }

// In BotHandler:
Pin(req PinRequest) error
Unpin(req UnpinRequest) error
```

`NoPinSupport` mixin returns `ErrUnsupported` for both. Adapters that
lack pin embed it. `NewAdapterMux` registers `POST /pin` + `POST /unpin`.

## HTTPChannel additions (`chanreg/httpchan.go`)

```go
func (h *HTTPChannel) Pin(ctx, jid, targetID string) error
func (h *HTTPChannel) Unpin(ctx, jid, targetID string, all bool) error
```

Both gate on `HasCap("pin")`. Existing `Delete` gets `HasCap("delete")`
guard. Same `postVerb` pattern as other social verbs.

## GatedFns additions (`ipc/ipc.go`)

```go
Pin   func(jid, targetID string) error
Unpin func(jid, targetID string, all bool) error
```

Gateway wires `g.pinOnJID` / `g.unpinOnJID` (mirrors `editOnJID`).

## core.Socializer additions

Extend with `Pin(ctx, jid, targetID) error` and
`Unpin(ctx, jid, targetID string, all bool) error`. `chanreg.HTTPChannel`
implements both.

## Platform coverage

| Verb      | slakd | teled  | discd | mastd | bskyd | reditd | whapd  | emaid |
| --------- | ----- | ------ | ----- | ----- | ----- | ------ | ------ | ----- |
| edit      | ✓     | ✓ ≤48h | ✓     | ✓     | ✗     | ✓      | ✓ ~15m | ✗     |
| delete    | ✓ own | ✓ own  | ✓ own | ✓     | ✓     | ✓      | ✓      | ✗     |
| pin/unpin | ✓     | ✓      | ✓     | ✗     | ✗     | ✗      | ✗      | ✗     |
| unpin_all | ✓     | ✓      | ✗     | ✗     | ✗     | ✗      | ✗      | ✗     |

- Slack: `pins.add`, `pins.remove`; unpin-all iterates `pins.list`.
- Discord: `ChannelMessagePin`, `ChannelMessageUnpin`; unpin-all returns
  `UnsupportedError` (no native bulk).
- Telegram: `pinChatMessage`, `unpinChatMessage`, `unpinAllChatMessages`.
- Mastodon/Bluesky/Reddit/Email: Go adapters embed `NoPinSupport`.
- WhatsApp (whapd, TypeScript): no `/pin` route; `HasCap("pin")` is false.
- Adapter cap maps: slakd/teled/discd add `"pin": true` and `"delete": true`
  (slakd already has `"delete": true`).

## ACL / audit

`pin_message`, `unpin_message`, `unpin_all` are new actions in
`grants/grantslib`. Default tier 0-2. Audit category `social`,
action `pin`/`unpin`, resource `<jid>/<targetID>`.

## Acceptance

- Three new MCP tools registered; `tools/list` shows them when an
  adapter advertising `"pin": true` is connected.
- `delete` MCP call returns `UnsupportedError` for adapters without
  `"delete"` cap.
- `slakd`, `teled`, `discd` declare `"pin": true` and serve
  `POST /pin` + `POST /unpin`.
- `mastd`, `bskyd`, `reditd`, `emaid` embed `NoPinSupport`.
- `slakd.Pin` / `slakd.Unpin` round-trip against `slackMock`
  (`pins.add`, `pins.remove`, `pins.list`).
- `make build && make lint && go test ./... -short` green.

## Out of scope

Bulk moderation, scheduled edits, cross-platform ID normalization,
status-board scaffolding (composition of existing primitives — agent
persists the message ID in its workspace).
