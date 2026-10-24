---
status: shipped
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
`fwd`, `quote`, `repost`, `dislike`, `edit`, `delete`, `pin`. Render capabilities
`thread_id` and `thread_root` declare existing-thread placement and creation
from a root message. Unknown
capabilities are ignored. `/like`, `/post` are not capability-gated —
adapters that can't satisfy them answer `501` with a structured
`{tool, platform, hint}` body.

Text JSON and file/voice multipart sends carry optional `thread_id` and
`thread_root`. An explicit existing thread wins over a root. The adapter must
materialize the declared thread before sending the actual content. A thread
failure returns an error; the adapter must not send the child at the parent.
`/edit` accepts optional `thread_id` for platforms whose edit URL needs the
thread channel. Slack edits still use the parent channel and message timestamp.
The shared placement policy is in `../6/46-turn-progress.md`.

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
healthy; `disconnected` does not. An outbound send to a deregistered
adapter fails, and the error reaches the caller. `chanreg` holds no
queue of its own. routd writes each bot row `pending` before it attempts
delivery, and marks it `sent` only when the adapter confirms
(`MarkBotPlatformID` stamps `platform_id` and `status` in one
statement). A 30s sweep re-sends every row that stays `pending`
(`routd/loop.go` maybeRetryOutbound). The `messages` row is the durable
record, so a routd restart keeps the backlog. A failed send also writes
its row: the MCP tools return the error to the agent and record the
undelivered message, so the sweep can own the retry.

**The sweep classifies before it retries.** `classifyStatus` marks a 4xx
adapter response `chanlib.ErrPermanent`; the adapter refuses the message
for a reason a retry cannot repair. Such a row dead-letters at once and
spends no attempt budget. Only a transient failure retries, and each
attempt increments `messages.delivery_attempts`. The row dead-letters
when the attempts reach `MAX_OUTBOUND_RETRY` (default 10), so a stuck
message cannot repeat forever — it burned about 2880 sends over 24h
before this bound.

**Outbound carries `idempotency_key`, and honoring it durably is part of
the adapter contract.** routd sends the bot row's stable id in the
`/send` and `/send-file` body on every attempt, first try and resend
alike. The adapter must deliver at most once per key: claim the key in
its OWN durable store BEFORE the platform call, settle it with the
platform id after, answer a resend of a settled key with the recorded id
and no platform call, and REFUSE (502) a key whose earlier attempt never
settled — the adapter cannot tell a lost response from a lost request,
so it reports the ambiguity and routd dead-letters visibly instead of
double-posting (BUGS `D5`). Go adapters get all of this from
`chanlib.NewAdapterMux` + `chanlib.OpenAdapterStore`; an adapter in another
language implements the same contract. **Every adapter is now inside it**,
whapd included (BUGS `L32`).

whapd's store is a DIRECTORY OF FILES, one per key
(`whapd/src/dedup.ts`), not a database: node:22 has no stable
`node:sqlite`, and the only operation that must be atomic is the CLAIM,
which `open(path, 'wx')` (O_CREAT|O_EXCL) already is at the filesystem
level. It lives under `WHAPD_STATE_DIR`, which the compose fragment
mounts — an unmounted ledger forgets every claim on recreate, which is
the same as having none.

Its failure taxonomy is ORDERING, not error parsing. Baileys exposes no
signal separating "WhatsApp refused it" from "the ack was lost", so
whapd never guesses: it HOLDS the claim on any error after the platform
call, and answers `409` (not 502) on the next attempt, because retrying
a refusal only burns routd's budget on the same answer. The transient
that would otherwise become a guaranteed dead-letter is caught EARLIER —
a socket that is not open refuses the send BEFORE any claim exists, so a
routd restart or a dropped link answers 502 with nothing recorded and
the sweep retries freely. That ordering is what `ErrNotDelivered`'s
claim-release does for the Go adapters.

**The adapter classifies its own platform failures** — the site that
sees the platform response holds the proof (same rule as runed's
terminal-vs-retryable, `12-turn-retry.md`). Four sentinel classes in
`chanlib`: `ErrInvalidRequest`/`ErrUnsupported` (caller-side, → 400/501,
routd dead-letters at once), `ErrPermanent` (the platform refuses this
message for good — a revoked credential, a deleted thread — also → 400,
because 4xx is the one answer routd reads as a refusal),
`ErrNotDelivered` (a TRANSIENT refusal that proves nothing was posted —
a 429, an application-level `ok:false` — the dedup claim is released
and the 502 lets the sweep retry on a fresh claim), and everything
else (ambiguous: a 5xx or a transport error may have posted, the claim
stays held, resends are refused until routd
dead-letters). Without the middle class every adapter-side rate limit
poisoned the claim and turned the sweep's whole backoff budget into
refusals — a plain 429 became a guaranteed dead-letter. Chunked senders
(teled, discd) release only while NO chunk has landed; after a delivered
chunk a re-post would duplicate it.

`failed` IS the dead-letter state. The row keeps its content,
`PendingOutbound` excludes it, and no sweep touches it again. An
operator sees the count on dashd's routd page next to `pending
outbound`.

## `chat_jid` and `sender` are different shapes

If the registration named by the inbound's `source` has the caller's
principal, routd uses that entry at ingress. Otherwise, routd selects the
entry by the caller's principal.

A chat JID names a CONVERSATION and must fall under a registered
`jid_prefixes` entry. A sender names a PERSON (`telegram:user/12345`)
and need not. Both are checked at ingress, by **different** rules
(`chanreg/chanreg.go:41`, `:56`):

- **`chat_jid`** → `Entry.Owns`, full prefix match. An adapter cannot
  inject conversations it does not serve.
- **`sender`** → `Entry.OwnsScheme`, scheme match only, coarser **on
  purpose**. An adapter is free to register a narrow _chat_ prefix
  (`telegram:<bot_id>/`) while its senders are `telegram:user/<id>`, so
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

## Inbound durability

An inbound is delivered exactly once, and which side holds it while routd
is unavailable depends on whether the adapter can ask the platform again.

**Cursor-gated adapters make the platform the queue.** `teled`, `bskyd`,
`reditd`, `linkd` and `emaid` advance their offset / seen-pointer / cursor
/ IMAP `\Seen` flag ONLY after routd answers 200. A failure leaves the
platform holding the message and the next poll re-fetches it. These
adapters keep no local queue: a second queue beside a working one is a
second thing to drift.

**Consume-on-arrival adapters retain locally.** `slakd` must answer Slack
`200 OK` within 3 seconds and Slack redelivers on non-2xx, so deferring
the ack until routd accepts turns one slow routd into several copies of
the message; `discd`'s gateway hands each event over exactly once. Both
call `RouterClient.RetainInbound`, which writes a failed inbound to the
adapter's own `inbound_pending` table (`chanlib/inbound.go`, schema
`chanlib/migrations/0002`) and re-delivers it on a 15s sweep with a
30s→15m backoff. The retained row carries the adapter's stable message
id, which is routd's `messages` primary key, so a re-delivery of a row
routd already took is an `INSERT OR IGNORE` no-op.

Retention covers UNAVAILABILITY, not refusal. A routd answer of 400 /
403 / 413 / 422 means routd read the payload and rejected it; the row is
dropped with an ERROR line, because replaying it would fail identically
forever. Everything else — a dial failure, a 401 while authd restarts, a
5xx, a 429 — is retained. `GET /health` reports `pending_inbound` while
the backlog is non-empty.

Before this, `slakd` lost every message that arrived while routd was
restarting — which the documented `systemctl restart arizuko_<instance>`
deploy does on every release — with one ERROR line as the trace (BUGS
`L25`).

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
(`ROUTING.md` § A target fragment sets the mode), `#announce`,
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
  `template/web/pub/arizuko/reference/jid.html`.
- Event types beyond messages get their own endpoint when needed; there
  is deliberately no generic `/v1/events`.
