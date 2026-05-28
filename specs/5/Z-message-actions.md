---
status: shipped
shipped: 2026-05-27
depends: [C-message-mcp, G-engagement, T-voice-synthesis]
relates-to: [5/5-uniform-mcp-rest]
---

# specs/5/Z — message actions: edit, delete, pin, unpin

All four verbs ship as MCP tools. `edit` and `delete` are both
cap-guarded in `chanreg/httpchan.go` (`HasCap("edit")` / `HasCap("delete")`);
pin/unpin gate on `HasCap("pin")`. Agents see their own message IDs in
the conversation XML context; no retrieval primitive needed.

## MCP tools

| Tool            | Args                             | Returns | Tier | Notes                           |
| --------------- | -------------------------------- | ------- | ---- | ------------------------------- |
| `edit`          | `chatJid`, `targetId`, `content` | `ok`    | 0–2  | Cap-guarded (`edit`).           |
| `delete`        | `chatJid`, `targetId`            | `ok`    | 0–2  | Cap-guarded (`delete`).         |
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

Both gate on `HasCap("pin")`. `Delete` gates on `HasCap("delete")`,
`Edit` on `HasCap("edit")`. Same `postVerb` pattern as other social verbs.

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
| delete    | ✓ own | ✓ own  | ✓ own | ✓     | ✓     | ✗      | ✓      | ✗     |
| pin/unpin | ✓     | ✓      | ✓     | ✗     | ✗     | ✗      | ✗      | ✗     |
| unpin_all | ✓     | ✓      | ✗     | ✗     | ✗     | ✗      | ✗      | ✗     |

- Slack: `pins.add`, `pins.remove`; unpin-all iterates `pins.list`.
- Discord: `ChannelMessagePin`, `ChannelMessageUnpin`; unpin-all returns
  `UnsupportedError` (no native bulk).
- Telegram: `pinChatMessage`, `unpinChatMessage`, `unpinAllChatMessages`.
- Mastodon/Bluesky/Reddit: no pin verbs — covered by the `NoSocial`
  embed (which includes `Pin`/`Unpin`). Email/LinkedIn embed
  `NoPinSupport` directly.
- WhatsApp (whapd, TypeScript): no `/pin` route; `HasCap("pin")` is false.
- Adapter cap maps: slakd/teled/discd advertise `"pin": true`; slakd/discd
  also `"delete": true`.
- Reddit `delete` is `✗` despite a working `Delete()` impl — reditd's cap
  map omits `"delete": true`, so `HasCap("delete")` gates it off. (Bug
  logged; advertising the cap is a behavior change, not a spec edit.)

## ACL / audit

`pin_message`, `unpin_message`, `unpin_all` are grant-gated like every
social verb (`grantslib.CheckAction`). As shipped, tier-0 holds them via
the `*` rule; tier-1/2 need an explicit grant rule — they are NOT in
`grantslib.platformActions`, so the per-tier default rules don't seed
them. (To make them tier-1/2 defaults, add the verbs to
`platformActions`; tracked as a cross-bucket follow-up.) Audit category
`social`, action `pin`/`unpin`, resource `<jid>/<targetID>`.

## Acceptance

- Three new MCP tools registered; `tools/list` shows them when an
  adapter advertising `"pin": true` is connected.
- `delete` MCP call returns `UnsupportedError` for adapters without
  `"delete"` cap.
- `slakd`, `teled`, `discd` declare `"pin": true` and serve
  `POST /pin` + `POST /unpin`.
- `mastd`, `bskyd`, `reditd` get pin defaults via existing `NoSocial`
  embed (which now includes `Pin`/`Unpin`). `emaid`, `linkd` embed
  `NoPinSupport` directly.
- `slakd.Pin` / `slakd.Unpin` round-trip against `slackMock`
  (`pins.add`, `pins.remove`, `pins.list`).
- `make build && make lint && go test ./... -short` green.

## Out of scope

Bulk moderation, scheduled edits, cross-platform ID normalization,
status-board scaffolding (composition of existing primitives — agent
persists the message ID in its workspace).
