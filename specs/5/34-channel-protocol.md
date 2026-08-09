---
status: defected
defects: [F61]
---

# Channel adapter protocol

Adapters connect to a platform and talk to routd over HTTP. Both sides
are HTTP servers. Wire types and the client live in `chanlib/`; the
in-memory registry in `chanreg/`.

## Decisions

**Self-registration, not static config.** routd does not manage adapter
lifecycle — adapters are started by compose, by hand, whatever. On
startup an adapter POSTs `/v1/channels/register`: "I handle these JID
prefixes, call me at this URL, here are my capabilities"
(`chanlib/chanlib.go:151`, `routd/channels.go:78`). Anyone can write an
adapter in any language; routd needs no per-adapter config entry.

**REST, not WebSocket.** Both directions are complete synchronous
transactions — adapter→routd `POST /v1/messages`, routd→adapter
`POST /send`. No connection state, no reconnect logic, no ordering
concerns. When `/send` returns 200 the message is on the platform.

**The registered `name` is written to `messages.source` on every
inbound.** That is the canonical record of which adapter received a
message and the primary input to picking a return adapter.

**Capabilities are declared, not probed.** routd refuses the outbound
call when the capability is false rather than discovering it from a
platform error. Gates live in `chanreg/httpchan.go` (`HasCap`):
`send_text`, `send_file`, `send_voice`, `typing`, `fetch_history`,
`fwd`, `quote`, `repost`, `dislike`, `edit`, `delete`, `pin`. Unknown
capabilities are ignored. `/like`, `/post` are not capability-gated —
adapters that can't satisfy them answer `501` with a structured
`{tool, platform, hint}` body.

**Adapters authenticate as the DAEMON, not the channel.** An adapter
exchanges its `AUTHD_SERVICE_KEY` bootstrap secret for a
`service:<daemon>` ES256 JWT (`AUTHD_SERVICE_NAME`, e.g. `teled`) and
presents it on **every** authenticated routd call — registration,
`/v1/messages`, `/v1/pane` (`chanlib.RouterClient.bearer`). Exchanging
under the _channel_ name (`telegram`) instead 401s and outbound dies
silently; that was a live krons outage. The registration call still
returns a session token, but only `Deregister` presents it now. There is
no shared symmetric secret — `CHANNEL_SECRET` is retired
([`1-auth-standalone.md`](1-auth-standalone.md)).

**The origin pin locks a name to its FIRST `(originIP, verified sub)`
pair**; a later registration of the same name from elsewhere gets 409
(`routd/channels.go:88-100`). The pin is the verified `sub`, **not** the
bearer: a `service:<daemon>` token rotates ~hourly, so pinning the raw
JWT would reject every legitimate re-register. The auth gate admits any
seeded service principal rather than one bound to `req.Name`, because
multi-account variants share one daemon principal across many channel
names — first-claim squatting by a valid principal is accepted, hijack
of an existing name is not.

**Health failure auto-deregisters.** routd polls `/health` every 30s;
three consecutive failures deregister the adapter
(`chanreg/health.go`). `stale` (adapter alive, platform quiet) counts as
healthy; `disconnected` does not. Outbound queues in the in-memory
`chanreg` outbox (`DrainOutbox`) until the adapter re-registers, bounded
at 1000 messages with per-message attempt caps so one dead JID cannot
starve every group on the channel.

## `chat_jid` and `sender` are different shapes

A chat JID names a CONVERSATION and must fall under a registered
`jid_prefixes` entry. A sender names a PERSON (`telegram:user/12345`)
and need not. Both are checked at ingress, by **different** rules
(`chanreg/chanreg.go:41`, `:56`):

- **`chat_jid`** → `Entry.Owns`, full prefix match. An adapter cannot
  inject conversations it does not serve.
- **`sender`** → `Entry.OwnsScheme`, scheme match only, coarser **on
  purpose**. An adapter is free to register a narrow _chat_ prefix
  (`telegram:mybot/`) while its senders are `telegram:user/<id>`, so
  reusing `Owns` here would reject every legitimate sender. Scheme
  ownership is still enough to stop one adapter asserting another
  platform's identities, or an OAuth-shaped sub no adapter owns.

The sender check is deliberately **not** nested inside the chat-JID
check. `web:`, `hook:` and bare-folder chat JIDs skip prefix ownership
entirely, so an adapter posting `chat_jid: "web:main"` with another
platform's sender would otherwise meet no check at all. Sender is
authorization-bearing — `routd/steer.go` passes it to `IsOperator`,
which reaches `auth.Authorize(…, "admin", "**")` — so an unvalidated
sender is an escalation, not a cosmetic error. A bare scheme-less sender
stays allowed: it cannot collide with an OAuth sub, which is always
`<provider>:<id>`.

## Attachments

Inbound: the adapter serves files on its own HTTP server and puts URLs
in `attachments[]`; routd's enricher fetches them into
`groups/<folder>/media/<YYYYMMDD>/` before dispatch
(`routd/enrich.go:69`). Adapters that already hold the bytes (whapd) inline
base64 in `attachments[].data` instead. teled proxies
`GET /files/{fileID}` because Telegram CDN URLs need a bot token and
expire; discd uses CDN URLs directly. Outbound: multipart POST to the
adapter's `/send-file` with an optional caption.

## Outbound and adapter resolution

Internal services (onbod, timed, dashd) are **not** channels — they never
register. They emit through `POST /v1/outbound` so routd resolves the
adapter and enforces auth rather than each service POSTing adapter
`/send` endpoints directly.

When several adapters share a JID prefix (primary `telegram` plus a
second bot), the return adapter is picked in this order
(`chanreg.Registry.Resolve`, `routd/deliver.go:80`):

1. Explicit `channel` field on `/v1/outbound`, if registered. An
   UNregistered name is `400 unknown_channel` — never a fall-through to
   2/3, which would deliver through the adapter the caller pinned away
   from (`routd/server.go` `handleOutbound`).
2. `messages.source` of the latest non-bot inbound on the chat
   (`store.LatestSource`) — always set once the chat has any inbound.
3. `chanreg.ForJID(jid)` — first prefix owner found; iteration order is
   non-deterministic, so callers needing exactness must pass the name.

The earlier design persisted a SQL `channels` table for cross-process
lookup. Retired in v0.28.0 (dropped in migration 0065) — the in-memory
`chanreg` registry is the only registry.

## Route targets

A route target is `folder[#fragment]` (`core.ParseRouteTarget`,
`core/types.go:107`). The fragment selects mode: `#observe`
([`B-route-mode-ingestion.md`](B-route-mode-ingestion.md)), `#announce`,
or anything else as a topic pin. There are no `folder:`, `daemon:` or
`builtin:` target prefixes. Slash-commands are dispatched directly by
`routd/steer.go` and never flow through routes. Resolution itself is
[`E-routd.md`](E-routd.md).

## The agent is an implicit channel

It never registers and its address is a folder, not a URL — but the
model is otherwise identical: route target receives the message,
responds, and the response routes back through the originating channel.
The split made this literal: `runed` owns the spawn
([`P-runed.md`](P-runed.md)) and `routd` is a pure router with no
container logic.

## Transport

The registered `url` determines transport. Anything `net/http` can dial
works unchanged — docker network, TCP, unix socket
(`http+unix://…`), vsock — because the protocol is pure HTTP and only
the dialer differs. Not built toward beyond the docker-network case; the
design is compatible.

## Related

- Multi-account adapters: [`R-multi-account.md`](R-multi-account.md),
  [`S-jid-format.md`](S-jid-format.md).
- Event types beyond messages get their own endpoint when needed; there
  is deliberately no generic `/v1/events`.
