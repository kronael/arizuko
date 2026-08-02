---
status: shipped
supersedes: [4/chanlib-refactor.md]
---

# Channel adapters

Each adapter is a standalone daemon that registers with `routd` over
HTTP, forwards inbound platform events as messages, and receives
outbound replies. The wire contract is
[`../5/34-channel-protocol.md`](../5/34-channel-protocol.md); this file is the
roster plus the per-adapter decisions that live nowhere else.

| Daemon | Platform  | JID prefix  | Language |
| ------ | --------- | ----------- | -------- |
| teled  | Telegram  | `telegram:` | Go       |
| discd  | Discord   | `discord:`  | Go       |
| slakd  | Slack     | `slack:`    | Go       |
| emaid  | Email     | `email:`    | Go       |
| mastd  | Mastodon  | `mastodon:` | Go       |
| bskyd  | Bluesky   | `bluesky:`  | Go       |
| reditd | Reddit    | `reddit:`   | Go       |
| linkd  | LinkedIn  | `linkedin:` | Go       |
| whapd  | WhatsApp  | `whatsapp:` | TS       |
| twitd  | Twitter/X | `twitter:`  | TS       |

Per-adapter env vars and verb-support tables live in each daemon's
`README.md` — duplicating them here only lets them drift. JID grammar
per platform: [`../5/S-jid-format.md`](../5/S-jid-format.md).
Chat-vs-social adapter boundary:
[`../7/41-social-adapter-model.md`](../7/41-social-adapter-model.md).

Container port is `:8080` for every adapter (compose pins
`LISTEN_ADDR=:8080`); source defaults differ.

## One shared library, thin adapters

`chanlib` owns everything an adapter does that is not
platform-specific: router registration, the outbound handler tree,
auth middleware, health, retry, graceful shutdown. A per-adapter
`main.go` is env load plus a `Start` hook — nothing else.

This is the settled answer to boilerplate duplication: five Go
adapters had each grown their own copy of the outbound endpoints and
startup sequence before the primitives were pulled into `chanlib`.
New adapters implement `BotHandler` and embed `NoSocial` /
`NoFileSender` / `NoVoiceSender` / `NoPinSupport` for what the
platform cannot do; they never re-implement transport. Current API:
[`chanlib/README.md`](../../chanlib/README.md). Operator-facing
adapter contract (health, staleness, dashboards):
[`../7/11-adapter-contract.md`](../7/11-adapter-contract.md).

The two TypeScript adapters reimplement the same wire format against
the same protocol, because Baileys and the Twitter client are
JS-only.

## emaid

IMAP IDLE push (with poll fallback) + SMTP STARTTLS replies. JID
format `email:<address>`. The IDLE connection is a persistent TLS
socket the server pushes EXISTS on; reconnect uses exponential
backoff. Polling is the fallback, not the design — mail latency is
the whole point of the adapter.

## whapd

JID format `whatsapp:<jid>`, where `<jid>` is whatever Baileys
returns — typically `<lid>@lid` for DMs (Baileys' opaque LID
identifier) and `<group-id>@g.us` for groups. **LIDs stay opaque:**
they are stable per account and arizuko does not translate them to
phone numbers.

**Registration resilience.** Router registration retries with
exponential backoff; whapd never calls `process.exit()` on register
failure. The prior fail-fast behavior caused a restart loop that
truncated `creds.json` mid-write during the next container kill,
because Baileys' `writeFile` is not atomic.

**Credential recovery.** On startup whapd calls `recoverCredsIfEmpty`
to restore from `creds.json.bak` when the live file is 0 bytes, and
`backupCreds` after each rewrite. Backups older than 3 days are not
trusted — those require a manual QR re-pair.
