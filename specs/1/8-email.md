# Email channel (v1) — shipped

IMAP inbound + SMTP outbound. Generic mail provider support.

## Enabling

Enabled by presence of `EMAIL_IMAP_HOST` + `EMAIL_ACCOUNT` + `EMAIL_PASSWORD`
in `.env`. Registered in `src/index.ts` same as other channels:

```typescript
if (EMAIL_IMAP_HOST) {
  const email = new EmailChannel(channelOpts);
  channels.push(email);
  await email.connect();
}
```

## Channel class

`src/channels/email.ts` — `EmailChannel implements Channel`:

- `name = 'email'`
- `connect()` — open IMAP, start IDLE loop. On any error, close and
  reconnect with exponential backoff (1s, 2s, 4s… cap 60s).
- `disconnect()` — close IMAP + SMTP connections
- `sendMessage(jid, text)` — look up outbound info from DB by
  `thread_id`, send SMTP reply

## Group routing

All email messages route to the first registered group with
`requires_trigger=0` (the `main` group). Same behaviour as the web channel.

## Source and sink

`EMAIL_ACCOUNT` is the bot's inbox and the channel identity. Anyone emailing
that address becomes a sender. Single account per instance.

- **Inbound**: IMAP IDLE on `EMAIL_ACCOUNT`'s INBOX. Server notifies on new
  mail; client issues `FETCH` immediately. Falls back to 60s poll if IDLE
  unsupported.
- **Outbound**: SMTP reply from `EMAIL_ACCOUNT` via `In-Reply-To` +
  `References` headers.

## IMAP IDLE loop

`imapflow` exposes `.idle()` which sends the IDLE command and resolves when
the server pushes `EXISTS` or `EXPUNGE`:

```typescript
while (connected) {
  await client.idle(); // blocks until server pushes
  await fetchUnseen();
}
```

`fetchUnseen()`: search INBOX for messages without the `\Seen` flag that are
absent from `email_threads` (double guard against re-fetch on restart). Mark
each fetched message `\Seen` immediately after storing.

## JID format

`email:<thread_id>` where `thread_id` = first 12 hex chars of
`sha256(root_message_id)`.

- New thread (no `In-Reply-To` match): hash the inbound `Message-ID` as
  thread root.
- Existing thread: look up `message_id → thread_id` in DB via `In-Reply-To`
  header, reuse `thread_id`.

## Sender identity

```
sender      = "email:user@example.com"   // from-address
sender_name = "Alice"                    // display name from From header,
                                         // or from-address if absent
```

Cross-channel linking in v2 — see `specs/3/9-identities.md`.

## Message metadata

Injected as context before the body:

```
From: Alice <alice@example.com>
Subject: Re: quarterly report
Date: Mon, 3 Mar 2026 14:22:10 +0000
To: bot@example.com
CC: bob@example.com
```

Attachments passed through the mime pipeline (`processAttachments`) same as
Telegram/WhatsApp.

## Threading

`email_threads` table:

```sql
CREATE TABLE IF NOT EXISTS email_threads (
  message_id   TEXT PRIMARY KEY,  -- every seen Message-ID
  thread_id    TEXT NOT NULL,     -- derived from root Message-ID
  from_address TEXT NOT NULL,     -- sender email (for outbound reply)
  root_msg_id  TEXT NOT NULL,     -- original Message-ID (for References)
  seen_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_email_thread ON email_threads(thread_id);
```

Added via `database.exec()` in `createSchema()` in `src/db.ts`.

Outbound `sendMessage(jid, text)`:

1. Extract `thread_id` from jid (`email:<thread_id>`).
2. Query `email_threads WHERE thread_id = ? LIMIT 1` → get `from_address`
   (reply-to) and `root_msg_id` (References header).
3. Send via SMTP with `In-Reply-To: <root_msg_id>` +
   `References: <root_msg_id>`.

## Config

```env
EMAIL_IMAP_HOST=imap.gmail.com
EMAIL_SMTP_HOST=smtp.gmail.com   # defaults to imap host with imap. → smtp.
EMAIL_ACCOUNT=bot@example.com
EMAIL_PASSWORD=app-password
```

Ports hardcoded: IMAP 993 (TLS), SMTP 587 (STARTTLS).

Exported from `src/config.ts`:

```typescript
export const EMAIL_IMAP_HOST =
  process.env.EMAIL_IMAP_HOST || envConfig.EMAIL_IMAP_HOST || '';
export const EMAIL_SMTP_HOST =
  process.env.EMAIL_SMTP_HOST ||
  envConfig.EMAIL_SMTP_HOST ||
  EMAIL_IMAP_HOST.replace('imap.', 'smtp.');
export const EMAIL_ACCOUNT =
  process.env.EMAIL_ACCOUNT || envConfig.EMAIL_ACCOUNT || '';
export const EMAIL_PASSWORD =
  process.env.EMAIL_PASSWORD || envConfig.EMAIL_PASSWORD || '';
```

Add `'EMAIL_IMAP_HOST', 'EMAIL_SMTP_HOST', 'EMAIL_ACCOUNT', 'EMAIL_PASSWORD'`
to the `readEnvFile()` call.

## Libraries

- `imapflow` — IMAP + IDLE (Promise-based)
- `nodemailer` — SMTP sending
- `mailparser` — parse raw RFC 2822 messages (addresses, text body)

## Out of scope (v1)

- Multi-account
- Folder routing (all mail → main group)
- Plain text body only (HTML stripped by mailparser)
