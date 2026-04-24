---
status: planned
phase: held
depends_on: [7/32-dynamic-channels.md]
---

# Pluggable Authentication / Handshake Service

A cross-adapter, user-facing pairing surface. Today each adapter has its
own ad-hoc credential mechanism: whapd via QR/pairing-code, teled via
`TELEGRAM_TOKEN` env, discd via `DISCORD_BOT_TOKEN`, mastd via
`MASTODON_ACCESS_TOKEN`, bskyd via handle+password, emaid via IMAP/SMTP
creds, OAuth providers via browser redirect. Onboarding a new channel
means editing `.env` files on the host. Non-operable by end users.

Goal: a single service that accepts the pairing flow for any supported
channel, guides the user through it (QR, pairing code, OAuth redirect,
token paste), persists the result in the DB, and signals the adapter
to pick it up.

## Concept — borrow from Anthropic's console

Anthropic's API console has a clean flow for "add a key / connect a
service": named surface, credential input with masking, test-call step,
persisted per-account with rotation + revocation. The arizuko analogue
lives inside dashd (authenticated UI) with per-user or per-group scope.

## Pluggable interface

Define an `AuthProvider` interface in Go that each adapter implements:

```
type AuthProvider interface {
    Name() string                          // "whatsapp", "telegram", "discord", ...
    Modes() []AuthMode                     // {qr, pairing_code, oauth, token_paste}
    Begin(req BeginRequest) (*Session, error)   // starts flow, returns session id
    Poll(sessionId string) (*Status, error)      // returns in-progress / done / failed
    Cancel(sessionId string) error
}
```

Each mode is discoverable. UI renders based on the modes list: QR shows
an image, pairing code asks for a phone number then displays the 8-char
code, OAuth redirects to the provider, token paste is just a form.

## UI

Lives in dashd at `/dash/channels/`. Tier-0 operators see all channels,
tier-1 group owners can add their own. The page lists existing channels
with their status (connected / stale / disconnected), plus an "Add
channel" button that opens a provider picker.

## Storage

`channels` DB table (from `7/32-dynamic-channels.md` — this spec
consumes that one, does not redefine it):

| column     | type    | note                            |
| ---------- | ------- | ------------------------------- |
| id         | integer | pk                              |
| adapter    | text    | "whatsapp" \| "telegram" \| ... |
| scope      | text    | "instance" \| "group:<folder>"  |
| label      | text    | user-supplied nickname          |
| secret_ref | text    | path into secrets backend       |
| status     | text    | "pairing" \| "active" \| "dead" |
| created_by | text    | user sub                        |
| created_at | text    | ISO                             |

Secrets live in an external backend (SOPS-encrypted file or vault), not
inline in the DB. `secret_ref` is the key; adapter reads the actual
credentials at runtime.

## Pairing session lifecycle

1. User clicks "Add WhatsApp" → dashd calls `AuthProvider.Begin` on a
   running whapd instance via /v1/auth/begin RPC.
2. whapd returns a session id + QR image bytes (or "awaiting phone
   number" prompt).
3. Browser polls `/dash/channels/<session>/status` every 2s until
   `done` or `failed`.
4. On `done`, whapd writes creds to the secrets backend under
   `secret_ref`, returns success. dashd inserts the `channels` row.
5. Adapter picks up the new row via DB notify or periodic reload,
   connects, starts reporting via /health.

## Out of scope (this spec)

- Multi-tenant secret isolation (key encryption per group) — assume
  single-tenant for v1, layer tenancy on top later.
- Auto-rotation of expiring tokens — separate spec.
- The `channels` table design itself — owned by `7/32-dynamic-channels`.

## Prerequisite: structure + docs reiteration

Before building this, arizuko's adapter/service architecture needs to
be reconsidered and re-documented. Current state:

- Seven Go adapters + one TypeScript. Each has its own main.go /
  main.ts with slightly different conventions (config loading,
  healthcheck pattern, register-with-router, graceful shutdown, etc).
- `chanlib` already unifies HTTP contract (auth, send, send-file,
  /health with `isConnected` + `lastInboundAt`). But pairing / auth
  is NOT in chanlib; it's per-adapter.
- ARCHITECTURE.md describes message flow but does not describe the
  credential lifecycle (how secrets reach an adapter, who rotates,
  what happens when they expire). EXTENDING.md has /health contract
  but no auth contract.

Tasks to clear the deck before implementing this spec:

1. **Audit each adapter's current credential source** (env var vs
   config file vs CLI flag). Produce a table.
2. **Unify healthcheck + auth responses** in chanlib. Extend
   `NewAdapterMux` with auth endpoints (`/v1/auth/begin`,
   `/v1/auth/poll`, `/v1/auth/cancel`).
3. **Write ARCHITECTURE.md "Credential lifecycle" section**: how
   secrets are read, where they live, how adapters react to changes.
4. **Write EXTENDING.md "Adding an adapter" section** that references
   the unified auth contract once step 2 lands.
5. **Pick a secrets backend**. Current `sops` install in `ant/` is
   a hint — could extend to gateway-level secrets. OR leave pluggable
   via interface, default to filesystem-SOPS.

Only after 1-5 is this spec's implementation work viable. Otherwise
the abstraction gets built on sand and every adapter becomes a special
case.

## Open questions

- Should dashd render QR images or should the adapter render them and
  dashd just proxy? (Adapter-rendered keeps QR generation out of dashd's
  dependency graph.)
- Per-user vs per-group vs per-instance channel scope — who "owns" a
  channel? Onboarding + grants implications.
- Handling of logout/revocation initiated from the platform side
  (Baileys' `code: 401` "session invalidated") — where does the alert
  surface? dashd? operator email? Slack webhook?
