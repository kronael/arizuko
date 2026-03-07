# Gmail channel (v2) — speculative

Gmail-specific upgrade over the generic email channel (v1).
Requires Google Cloud project + Gmail API enabled.

## Why v2

Generic IMAP IDLE works for Gmail but has limitations:

- IDLE disconnects every 29min (Gmail enforced), requires constant reconnect
- OAuth2 refresh is awkward over IMAP

Gmail API + Pub/Sub solves both cleanly.

## Enabling

Enabled when `GMAIL_CREDENTIALS_FILE` is set. Takes precedence over
generic email channel if both configured.

```typescript
if (GMAIL_CREDENTIALS_FILE) {
  const gmail = new GmailChannel(channelOpts);
  channels.push(gmail);
  await gmail.connect();
} else if (EMAIL_IMAP_HOST) {
  ...
}
```

## Inbound: Gmail Pub/Sub push

1. Google publishes new-mail notifications to a Cloud Pub/Sub topic.
2. Gateway uses `@google-cloud/pubsub` pull subscription
   (no public webhook needed — pull works behind NAT/firewall).
3. On notification: fetch message via Gmail API, parse, dispatch.

Setup (one-time, via `kanipi gmail setup`):

- Create Pub/Sub topic + subscription
- Grant `gmail-api-push@system.gserviceaccount.com` publisher role
- Call `gmail.users.watch()` to register the push topic
- `watch()` expires after 7 days — renew weekly via cron

## Outbound

Gmail API `messages.send()` with RFC 2822 raw message.
Threading: set `threadId` field on send (Gmail native — no
`In-Reply-To` header manipulation needed).

## Auth

OAuth2 via `googleapis`. Token stored in `store/gmail-token.json`.
Refreshed automatically by the Google client library.

First-time: `kanipi gmail auth` → opens browser for consent, saves token.

## JID format

`gmail:<thread_id>` where `thread_id` = Gmail native thread ID.
No hashing needed — Gmail IDs are stable.

## Libraries

- `googleapis` — Gmail API + OAuth2
- `@google-cloud/pubsub` — Pub/Sub pull subscription

## Config

```env
GMAIL_CREDENTIALS_FILE=/srv/data/kanipi_name/store/gmail-credentials.json
GMAIL_PUBSUB_TOPIC=projects/my-project/topics/gmail-push
GMAIL_PUBSUB_SUBSCRIPTION=projects/my-project/subscriptions/gmail-push-sub
```

Exported from `config.ts`:

```typescript
export const GMAIL_CREDENTIALS_FILE = process.env.GMAIL_CREDENTIALS_FILE || '';
export const GMAIL_PUBSUB_TOPIC = process.env.GMAIL_PUBSUB_TOPIC || '';
export const GMAIL_PUBSUB_SUBSCRIPTION =
  process.env.GMAIL_PUBSUB_SUBSCRIPTION || '';
```

## Scope

- Pub/Sub pull for push delivery (~1s latency)
- OAuth2 with auto-refresh
- Gmail native threading (threadId)
- Plain text + HTML body extraction
- No attachment handling
- Single account per instance
