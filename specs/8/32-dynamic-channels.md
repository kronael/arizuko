---
status: unshipped
phase: next
---

# Dynamic Channels — DB-backed adapters, dashboard-managed credentials

Today adapters are static: each adapter is a compose service, credentials
live in `.env`, and adding/changing a channel means editing files and
regenerating compose. The operator flow for onboarding a Bluesky or a
Mastodon account is "edit `.env`, `arizuko run <inst>`, restart
service" — a wall that stops non-technical operators.

This spec makes channels first-class DB rows and moves credential input
into `dashd`. `arizuko pair` (interactive login) also gets a web port.

## Motivation

- Launching new socials today requires shell + file editing.
- QR / phone-code pairing (WhatsApp, Telegram userbot) is CLI-only.
- Per-instance credential sprawl across `.env` files.
- No audit trail for who added which channel when.

## Model

New table `channels`:

| column     | type     | note                                            |
| ---------- | -------- | ----------------------------------------------- |
| id         | integer  | pk                                              |
| kind       | text     | `telegram`, `bluesky`, `mastodon`, `discord`, … |
| label      | text     | operator-chosen, unique per instance            |
| status     | text     | `configured`, `paired`, `running`, `error`      |
| creds_json | blob     | encrypted at rest (AES-GCM with `AUTH_SECRET`)  |
| created_at | datetime |                                                 |
| updated_at | datetime |                                                 |
| last_error | text     | nullable, surfaced in dashboard                 |

`creds_json` schema is per-`kind`, validated by the adapter's config
loader — not by the dashboard. Adapters publish a JSON schema the
dashboard renders as a form.

## Compose generation

Two options, pick one:

**A. Static compose, dynamic routing.** One adapter container per
kind runs as a supervisor process; it forks a worker goroutine per
`channels` row of its kind. `compose/` stops generating per-channel
services. Simpler ops, harder to isolate per-account failures.

**B. Dynamic compose.** `arizuko generate` queries `channels` and
emits one service per row. Adding a channel writes a DB row and
triggers a compose regenerate + targeted `docker compose up -d`.
Keeps isolation; requires careful lifecycle in `dashd`.

Recommendation: **A for socials that already hold a single logical
identity per process (bsky, mast, reddit, email), B for chat
platforms where one bot token == one service today (discord, telegram,
whatsapp)**. This is a hybrid but matches the platforms' own shapes.

## Dashboard surface (`dashd`)

New page `/channels`:

- list: kind, label, status, last-error
- "add channel" → pick kind → form rendered from adapter schema
- per-row actions: edit, pair (interactive), restart, disable, delete
- pairing opens a WebSocket to the adapter's pair endpoint — QR image,
  phone-code input, "connected" event

## Pairing — the web version of `arizuko pair`

`arizuko pair <inst> <svc>` runs the adapter with stdin/stdout attached
for interactive login. To move this to the dashboard:

1. Adapter exposes a `/pair` endpoint (WebSocket) behind `CHANNEL_SECRET`.
2. `dashd` proxies the WebSocket with operator auth.
3. Pair flow emits structured events: `qr`, `prompt`, `ok`, `error`.
4. On `ok`, adapter writes session to `channels.creds_json`, flips
   status to `paired`, exits pair mode, resumes normal operation.

The CLI stays as a fallback — same endpoint, different client.

## Encryption of creds_json

`AUTH_SECRET` already seeds JWT signing. Derive a separate AES-GCM key
via HKDF-SHA256 with a fixed `info` string (`arizuko-channel-creds-v1`).
Never store creds in plaintext in the DB. Never log them.

Rotation: adding a second `AUTH_SECRET_OLD` env var lets the adapter
decrypt-with-old, re-encrypt-with-new, atomically.

## Migration from `.env`

On first boot after this ships, `onbod` (or a one-shot migration) reads
the existing per-adapter env vars and writes them as `channels` rows.
Old env vars keep working for one release, then get removed.

## Open questions

- Per-channel rate limits: surface in dashboard or leave in `.env`?
- Do we want a "channel health" probe (last inbound event, last send
  attempt) visible on the card? Probably yes; cheap.
- Multi-tenant instances: should an operator see only their own
  channels, or all? `user_groups` ACL already covers this; reuse.

## Non-goals

- Self-service channel creation by end-users. This is operator-only.
- Dynamic adapter code loading. Adapters remain compiled-in.
- Swapping kinds at runtime (e.g. "convert bsky to mast"). Delete + add.

## Dependencies

- `AUTH_SECRET` is already required; this spec extends its uses.
- `dashd` exists; needs the `/channels` page and pairing WebSocket proxy.
- Each adapter needs: (a) JSON schema publication, (b) `/pair`
  endpoint, (c) DB-read path for credentials.

## Rough effort

- Schema + migration: 0.5d
- Encryption helpers: 0.5d
- Dashboard page + forms: 2d
- Per-adapter refactor (6–8 adapters): 3–4d total
- Pairing WebSocket: 1d (one adapter first, then templated)

Total: ~2 weeks for all adapters; a single adapter (say Bluesky) can
ship in 3–4 days as the proof.
