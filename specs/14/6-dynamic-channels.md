---
status: draft
phase: next
---

# Dynamic channels — DB-backed adapters, dashboard-managed credentials

Adapters are static today: each is a compose service, credentials live in
`.env`, and adding or changing a channel means editing files and
regenerating compose. Onboarding a Bluesky or Mastodon account is "edit
`.env`, `arizuko run`, restart" — a wall that stops non-technical operators,
leaves no audit trail for who added what, and makes QR/phone-code pairing
CLI-only.

This spec makes channels first-class DB rows and moves credential entry into
`dashd`.

## Model

A `channels` table: `kind`, operator-chosen `label` (unique per instance),
`status` (`configured` / `paired` / `running` / `error`), `creds_json`
encrypted at rest, `last_error` surfaced in the dashboard, timestamps.

`creds_json`'s schema is per-`kind` and **validated by the adapter's config
loader, not by the dashboard** — the adapter publishes a JSON schema the
dashboard renders as a form. The dashboard must never grow per-kind
knowledge; that is the drift this design exists to avoid.

## The one real decision: how compose maps to rows

Two shapes, and the recommendation is a deliberate hybrid:

- **A — static compose, dynamic routing.** One container per _kind_ runs as
  a supervisor and forks a worker per `channels` row. Simpler ops; harder to
  isolate a single account's failure.
- **B — dynamic compose.** Generation queries `channels` and emits one
  service per row; adding a channel writes a row, regenerates, and brings up
  the one service. Keeps isolation; needs careful lifecycle in dashd.

**Take A for socials that already hold a single logical identity per process
(bsky, mastodon, reddit, email) and B for chat platforms where one bot token
already equals one service (discord, telegram, whatsapp).** It is a hybrid
because the platforms themselves are shaped differently — forcing one answer
would either lose per-account isolation where it matters or multiply
containers where it does not.

## Pairing moves to the web

`arizuko pair` runs an adapter with stdin/stdout attached for interactive
login. To put it in the dashboard, the adapter exposes a `/pair` WebSocket
that dashd proxies with operator auth, emitting structured events (`qr`,
`prompt`, `ok`, `error`). On `ok` the adapter writes the session into its
`channels` row, flips status, and resumes normal operation.

The CLI stays as a fallback against **the same endpoint** — a different
client, not a second code path. The terminal side of this is
[7-auth-tunneling](7-auth-tunneling.md)'s `arizuko auth`.

## Credential encryption

`AUTH_SECRET` already seeds JWT signing; derive a separate AES-GCM key from
it via HKDF with a fixed `info` string rather than reusing the signing key.
Never store creds in plaintext, never log them. Rotation is an
`AUTH_SECRET_OLD` var enabling decrypt-with-old / re-encrypt-with-new.

This is the same scheme [7-auth-tunneling](7-auth-tunneling.md) needs —
whichever ships first defines the helper; the other adopts it. One helper.

## Migration from `.env`

On first boot after this ships, a one-shot reads the existing per-adapter
env vars into `channels` rows. Old env vars keep working for one release,
then go.

## Non-goals

- Self-service channel creation by end users — operator-only.
- Dynamic adapter code loading — adapters stay compiled in.
- Swapping a row's `kind` at runtime — delete and add.
