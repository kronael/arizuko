---
status: draft
---

<!-- trimmed 2026-03-15: TS removed, rich facts only -->

# Email Channel

IMAP inbound + SMTP outbound. Single account per instance.

## JID format

`email:<thread_id>` where `thread_id` = first 12 hex chars of
`sha256(root_message_id)`. New threads hash the inbound `Message-ID`;
replies look up existing thread via `In-Reply-To`.

## Threading table

`email_threads` maps `message_id -> thread_id -> root_msg_id`.
Outbound replies use stored `from_address` and `root_msg_id` for
`In-Reply-To` + `References` headers.

## SMTP reply contract

Outbound uses `In-Reply-To` + `References` headers for threading.
Sender identity: `sender = "email:user@example.com"`, `sender_name`
from From header display name.

## Design

- Inbound: IMAP IDLE on INBOX, falls back to 60s poll. Double guard
  (SEEN flag + DB check). Exponential backoff on errors.
- All email routes to root group.
- Ports: IMAP 993 (TLS), SMTP 587 (STARTTLS).
