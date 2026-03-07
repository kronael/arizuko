# Facebook Messenger channel (v2) — speculative

Inbound/outbound via fca-unofficial — no Graph API, no app review.
Same approach as Baileys for WhatsApp: unofficial, cookie-based auth.

## Source and sink

- **Inbound**: `api.listenMqtt(callback)` — persistent MQTT connection, fires on new messages
- **Outbound**: `api.sendMessage(text, threadID)`
- Enabled by: `FACEBOOK_EMAIL` + `FACEBOOK_PASSWORD` in .env
- AppState (session cookies) cached to `store/fb-appstate.json` after first login

## JID format

`fb:{threadID}`

threadID is Facebook's internal thread identifier, stable across sessions.

## Library

`fca-unofficial` — reverse-engineered Facebook Chat API for Node.js.
No webhook, no public URL needed.

## Auth flow

1. First run: login with email + password → save AppState to `store/fb-appstate.json`
2. Subsequent runs: load AppState, skip login
3. If AppState stale: fall back to password login, refresh AppState

## Risk

Medium. Violates Meta ToS. Use a dedicated account.
Less aggressive enforcement than Twitter but account bans are possible.
Do not use personal credentials.

## Phase 1 — feed events (synthetic inbound)

Beyond DMs, adapter polls for:

- `mention` — @mention in a post
- `timeline_post` — posts from watched accounts
- `reply_to_us` — replies to posts we made

All converted to IPC input messages with an `event_type` field.

## Outbound action types

Outbound IPC message `type` field controls action:

- `reply` — reply to a thread
- `react` — react to a post
- `post` — new post

## Config

```env
FACEBOOK_EMAIL=...
FACEBOOK_PASSWORD=...
FACEBOOK_WATCH_ACCOUNTS=...   # comma-separated, phase 1
```

## V1 scope (DMs only)

- Text messages only (no attachments)
- DMs only (no group chats, no feed)
- AppState cached on first login, reused on restart
- Graceful degradation: disable channel on repeated auth failures

## V2 scope (feed)

- Timeline + mention monitoring (phase 1 feed events above)
- Group chat support
