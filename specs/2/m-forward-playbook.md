---
status: draft
---

# Forward — Operator Playbook

When the bot should reach for `forward` instead of `send` / `reply` /
`repost`, and how to teach a per-group persona to do so.

Mechanism is shipped: `ipc/ipc.go:1040` registers the MCP tool;
`chanlib.Socializer.Forward` is the capability; per-adapter impl in
`teled/bot.go:422`, `discd/bot.go:454`, `slakd/bot.go:907`,
`mastd/client.go:275`, `emaid/server.go:53`,
`whapd/src/server.ts:346`. Verb table in
`specs/4/9-gated.md:211`; tool desc in `ipc/ipc.go:1042`. This spec is
operator-facing: when to use, when not to, what to put in `CLAUDE.md`.

## What `forward` is for

Redeliver an existing inbound message to a different chat with
provenance preserved. The bot is the carrier, not the author. Five
operator-justified shapes:

1. **Cross-channel escalation.** A user reports a bug on Telegram;
   the bot forwards to `corp/eng/sre` Discord so the on-call sees
   the raw report (sender, timestamp, attachments) without
   paraphrase distortion.
2. **Quarantine review.** `atlas/quarantine` (see
   `specs/8/17-emaid-auth.md`) holds unverified email; operator
   reacts ✅; bot forwards the original to `atlas/inbox` for normal
   triage. Provenance matters — paraphrase loses headers.
3. **Daily digest relay.** Bot picks 3 messages from a noisy
   `news/feed` group, forwards each into `solo/inbox` with a
   one-line `comment` framing why. Reader gets the source intact.
4. **Asymmetric channel hand-off.** Customer DM's the WhatsApp
   support number; bot forwards into the Slack `#cx-tickets`
   channel where the human team works. The Slack thread becomes
   the work record; WhatsApp stays the customer conversation.
5. **Observed-context relay.** Operator adds a `#observe` route
   (`specs/5/B-route-mode-ingestion.md`) that stores but doesn't
   fire. Bot, called later, forwards the relevant observed message
   into the active thread as evidence.

## When `forward` is wrong

- **Replying to the user.** Use `reply` (same chat) or `send`
  (different chat). Forward is for moving a message you didn't
  author; replies are first-class authorship.
- **Sharing your own previous output.** Just `send` the content
  again, or `quote` if the platform has it. Don't forward your own
  message — it reads as a bot bug.
- **Crossing a privacy boundary.** A message in
  `corp/eng/secrets` (group with `open=0`, restricted grants)
  must not forward into a wider group. Gateway does not enforce
  this; the persona must. The platform stamp ("forwarded from
  …") leaks the source name even if the body is harmless.
- **Public amplification.** Use `repost` (Mastodon boost, Bluesky
  repost, X retweet). Forward is point-to-point; repost is
  one-to-feed. Tool desc at `ipc/ipc.go:1070` already says this.

## Per-adapter reality

Only **Telegram** has a true native forward (preserves the
"Forwarded from X" header). Everywhere else, `forward` degrades:

| Adapter | Behavior                                                               |
| ------- | ---------------------------------------------------------------------- |
| teled   | native; `source_msg_id` must be `"<sourceChatJid>\|<msgId>"`           |
| whapd   | degraded `send` with Baileys `isForwarded` flag + `forwardingScore: 1` |
| discd   | `Unsupported` — hint to use `send(...— from <source>)`                 |
| slakd   | `Unsupported` — hint to use `send(...— from <source>)`                 |
| mastd   | `Unsupported` — hint to use `repost` or `post(... <permalink>)`        |
| emaid   | `Unsupported` — hint to use `send` with `---- Forwarded message ----`  |
| bskyd   | not implemented; same `Unsupported` shape expected                     |
| reditd  | not implemented; same                                                  |
| twitd   | hint-only (`specs/2/k-twitter-adapter.md:76`); no DM forward primitive |

Implication for personas: if a forward fails with `Unsupported`,
**don't retry as another verb without rewriting**. The hint tells
the agent the right fallback (`send` with attribution, or `repost`).
Forwarding a Mastodon message to a Slack chat means "send with a
permalink", not "boost on Mastodon".

## Persona snippet

For a `slack-team` or `corp/eng/sre` group whose operator wants
cross-channel escalation, paste into the group's `CLAUDE.md`:

```markdown
## Forwarding

Use `forward` only to relay someone else's message verbatim across
chats — escalating a bug report from Telegram to this Slack channel,
or pushing a quarantined email into the triage group. Always include
a one-sentence `comment` framing why this lands here. Never forward
your own previous output (`send` instead) and never forward across a
privacy boundary (any group with `open=0` stays put). If the target
adapter rejects forward as unsupported, follow the error hint — do
not paraphrase silently.
```

The snippet names the verb, the two anti-patterns, and the
degradation rule in four sentences. Operators adapt the group
names; the rest is platform-mechanical.

## Open questions

1. Should `gateway.forwardToJID` (gateway.go:1368) check the source
   group's `open` flag and refuse cross-boundary forwards? Today
   the check is persona discipline only. A platform-side guard
   would be strict-not-magical (mechanical refusal, no inference)
   but requires resolving source JID → group, which the gateway
   does not currently do for the forward verb. Defer until a real
   leak occurs.
