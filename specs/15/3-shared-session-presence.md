---
status: draft
depends: [J-sse]
---

# Shared-session presence

"Multiplayer" means more than one human in the same conversation. When
two teammates watch a slink chat while the agent works, neither can see
the other is there, nor that a turn is in flight — the session feels
single-player even when it isn't. arizuko has no presence primitive
anywhere (grep: zero `presence`/`participant`/`roster` in the code).

## What we steal

Centaur's multiplayer framing — one shared agent session multiple
people collaborate in — plus its live-progress streaming so each
participant sees the turn advancing. We take the _visible-collaboration_
half (who is here, agent is working), not Centaur's Slack-thread session
model (arizuko already has topics + engagement).

## arizuko-shaped design

Presence is ephemeral, per-(folder, topic), and lives entirely in webd's
existing hub — it is **not** persisted (strict, not magical: presence is
liveness, not state; nothing to reconcile, nothing to put in git).

- The hub already keys subscriptions on `folder/topic` (`5/J`). Track,
  per key, the set of currently-attached subscribers with a coarse
  display label (the slink token's group identity, or the proxyd-signed
  user sub for authed streams — never a finer-grained identity than the
  group auth boundary already exposes).
- New SSE frame kind `presence`: emitted to a key's subscribers when the
  attached set changes (join/leave) and on a heartbeat (`PRESENCE_TTL`,
  default 45s; a subscriber that misses its heartbeat is dropped from
  the set). Payload: count + labels.
- New SSE frame kind `working`: routd already knows a turn is in flight
  for a (folder, topic) (its per-folder queue serializes turns, `5/E`).
  When routd starts/ends a turn it publishes a `working{active:bool}`
  hub event on the chat's key — reusing the same publish path as message
  frames, so it rides `15/1`'s durable stream for late joiners
  (`working:true` replays so a reconnecting client immediately knows the
  agent is busy).

The auth boundary is unchanged: presence is visible to exactly whoever
can already open the stream (group membership / slink token). No
per-sender scoping inside a group — that fights the shared-context model
(`5/J`).

## Out of scope

- Typing indicators for _human_ participants (that's `13/9-slink-typing`).
- Cursor/selection sharing or any CRDT co-editing — this is presence +
  activity, not collaborative editing.
- Presence across channel adapters (Slack/Telegram have their own native
  presence; this is the web/slink surface only).
- Persisted "last seen" / analytics — presence is liveness, discarded on
  disconnect.
