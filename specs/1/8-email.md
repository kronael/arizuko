---
status: shipped
---

# Email channel (v1) — shipped

IMAP inbound + SMTP outbound. Generic mail provider support.

## Enabling

Enabled by `EMAIL_IMAP_HOST` + `EMAIL_ACCOUNT` + `EMAIL_PASSWORD` in `.env`.

## Design

- **Inbound**: IMAP IDLE on INBOX, falls back to 60s poll. Double guard
  against re-fetch (SEEN flag + DB check). Exponential backoff on errors.
- **Outbound**: SMTP reply with `In-Reply-To` + `References` headers.
- All email routes to the main group (same as web channel).
- Single account per instance.

## JID format

`email:<thread_id>` where `thread_id` = first 12 hex chars of
`sha256(root_message_id)`. New threads hash the inbound `Message-ID`;
replies look up the existing thread via `In-Reply-To`.

## Sender identity

`sender = "email:user@example.com"`, `sender_name` from From header display
name. Cross-channel linking — see `specs/5/9-identities.md`.

## Threading

`email_threads` table maps `message_id → thread_id → root_msg_id`.
Outbound replies use stored `from_address` and `root_msg_id` for headers.

## Config

```env
EMAIL_IMAP_HOST=imap.gmail.com
EMAIL_SMTP_HOST=smtp.gmail.com   # defaults to imap host with imap. → smtp.
EMAIL_ACCOUNT=bot@example.com
EMAIL_PASSWORD=app-password
```

Ports: IMAP 993 (TLS), SMTP 587 (STARTTLS).

## Libraries

- `imapflow` — IMAP + IDLE
- `nodemailer` — SMTP
- `mailparser` — RFC 2822 parsing

## Out of scope (v1)

- Multi-account
- Folder routing (all mail → main group)
- Plain text body only (HTML stripped)
