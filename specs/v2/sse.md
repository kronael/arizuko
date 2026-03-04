# SSE Stream — v2

## Current behaviour (v0.2.0)

`GET /_REDACTED/stream?group=<folder>` opens an SSE connection. The gateway
broadcasts every agent response to **all listeners** on that group folder,
regardless of who sent the triggering message.

`/_REDACTED/` is in `PUBLIC_PREFIXES` — basic auth is bypassed. Anyone who
knows the URL can subscribe to the stream.

This is intentional for the public widget model: `REDACTED.js` embeds on a
public page, all visitors see all agent responses. Fine for a public chatbot.

## Problem

In a multi-user or semi-private context (authenticated slink senders, web UI
with `REDACTED_USERS`), a low-privilege or anonymous user connected to the stream
can read replies to messages sent by authenticated users. The reply may contain
information scoped to the sender's request.

## v2 design

Scope SSE responses to the sender's identity rather than broadcasting to all.

### Option A — per-sender stream (recommended)

- SSE connection carries a `sub` claim (JWT) or an opaque session token.
- Gateway tags each outbound message with the `sub` of the original sender.
- Only the matching SSE connection receives the event.

Implementation sketch:

- `addSseListener(group, res, sub?)` — store sub alongside response
- `sendMessage` receives sender sub; only writes to listeners where sub matches
  (or sub is absent, for anonymous broadcast)
- `/pub/s/:token` posts include optional `Authorization` header → sub extracted
  and stored on the queued message

### Option B — separate streams per sub

- `/_REDACTED/stream?group=<n>&sub=<sub>` — gateway only pushes to that sub.
- Simpler but sub is visible in the URL (logs, referrers).

### Auth on the stream endpoint

Move `/_REDACTED/stream` out of `PUBLIC_PREFIXES` or add JWT check:

- Anonymous groups: keep open
- Authenticated groups (`AUTH_SECRET` set): require valid JWT on the stream
  request (`?token=<jwt>` or `Authorization` header)

## Not in scope

- Presence (who is online)
- Message history replay on reconnect (use DB query instead)
