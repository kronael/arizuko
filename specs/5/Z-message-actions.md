---
status: shipped
shipped: 2026-05-27
depends: [C-message-mcp, G-engagement, T-voice-synthesis]
relates-to: [5/17-openapi-mcp]
---

# specs/5/Z — message actions: edit, delete, pin, unpin

`edit`, `delete`, `pin_message`, `unpin_message`, `unpin_all` ship as MCP
tools (`ipc/ipc.go:1369-1400`) with REST twins at
`POST /v1/turns/{turn_id}/{edit,delete,pin,unpin}`.

## Decisions

**Capability-gated at the registry, not discovered from platform
errors.** `chanreg/httpchan.go` refuses the call when the adapter didn't
advertise the capability — `HasCap("edit")` (`:461`),
`HasCap("delete")` (`:397`), `HasCap("pin")` for both pin verbs
(`:405`, `:413`). An adapter that lacks the platform primitive answers
`501` and the agent receives a structured
`UnsupportedError{tool, platform, hint}` rather than a bare failure.

**`unpin_all` is `/unpin` with `all:true`, not its own endpoint.** One
platform mechanism, one route; the separate tool name exists because the
intent differs. Same reasoning as `dislike` → `/like` with an emoji
([`E-routd.md`](E-routd.md) § verb→path exceptions).

**No retrieval primitive for target ids.** Agents already see their own
message ids in the conversation XML context, so `edit`/`delete`/`pin`
take `targetId` directly instead of a lookup tool being added.

**Missing support is a mixin, not a per-adapter stub.** `NoSocial`
(`chanlib/handler.go:151`) covers `Pin`/`Unpin` for adapters with no
social verbs at all; `NoPinSupport` (`:225`) is for adapters that have
social verbs but no pins (email, linkd). Plumbing:
`chanlib.BotHandler.Pin/Unpin` + `PinRequest`/`UnpinRequest`,
`chanreg.HTTPChannel.Pin/Unpin`, `core.Socializer`, `ipc.GatedFns`.

## Platform coverage

| Verb      | slakd | teled  | discd | mastd | bskyd | reditd | whapd  | emaid |
| --------- | ----- | ------ | ----- | ----- | ----- | ------ | ------ | ----- |
| edit      | ✓     | ✓ ≤48h | ✓     | ✓     | ✗     | ✓      | ✓ ~15m | ✗     |
| delete    | ✓ own | ✓ own  | ✓ own | ✓     | ✓     | ✓ own  | ✓      | ✗     |
| pin/unpin | ✓     | ✓      | ✓     | ✗     | ✗     | ✗      | ✗      | ✗     |
| unpin_all | ✓     | ✓      | ✗     | ✗     | ✗     | ✗      | ✗      | ✗     |

Slack `pins.add`/`pins.remove`, unpin-all iterating `pins.list`; Discord
`ChannelMessagePin`/`Unpin` with no bulk primitive; Telegram
`pinChatMessage`/`unpinChatMessage`/`unpinAllChatMessages`.

## Authorization + audit

One evaluator over ACL rows ([`32-acl-unified.md`](32-acl-unified.md),
`auth/authorize.go:25`) — the per-tier grant defaults and the
`grantslib` package this spec originally referenced are gone.

Every message action writes one `audit_log` row
(`routd/social_audit.go`). Category **`agent`**, not the `social` this
spec once named: the category enum is closed (`audit/log.go`) and has no
`social` member, and `audit/PLAN.md` scopes `mutation` to registry-
resource CRUD — the operator's config-change trail — while `agent` is
the per-turn band these belong to. Action `message.<verb>`, resource
`<jid>/<targetID>`, actor `folder:<folder>`, surface `mcp` on the socket
and `rest` on the twin; `unpin_all` is `message.unpin` with
`{"all":true}` in `params_summary`, matching the one-mechanism-one-route
decision above. Both faces call the one renderer, so they cannot drift
into recording different things.

The row is the only durable trace these verbs leave: they append no
`messages` row, so the "the row IS the record" reason `audit/PLAN.md`
§ SKIP gives for not auditing `reply`/`send` does not reach them — an
edited or deleted platform message would otherwise vanish with nothing
recording that it happened. The edit row names the message, never the
new content.

## Out of scope

Bulk moderation, scheduled edits, cross-platform id normalization, and
status-board scaffolding — the last is a composition of existing
primitives (the agent persists the message id in its workspace and
`edit`s that message in place).
