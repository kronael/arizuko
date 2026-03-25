# Multi-Account Channels

**Status**: planned (2026-03-25)

## Problem

One channel adapter instance = one account. To run multiple Telegram bots,
X accounts, Discord apps, etc., there is no mechanism.

## Design

**No new code.** Multiple accounts = multiple service instances with different
credentials. The compose generator already supports arbitrary service TOML files
in `services/`.

### Adding a second Telegram bot

```toml
# services/teled-support.toml
image = "arizuko:latest"
entrypoint = ["teled"]

[environment]
ROUTER_URL = "http://gated:${API_PORT}"
TELEGRAM_BOT_TOKEN = "${TELEGRAM_SUPPORT_BOT_TOKEN}"
CHANNEL_SECRET = "${CHANNEL_SECRET}"
ASSISTANT_NAME = "${ASSISTANT_NAME}"
CHANNEL_ACCOUNT = "support"
LISTEN_ADDR = ":9002"
LISTEN_URL = "http://teled-support:9002"
```

Each instance sets `CHANNEL_ACCOUNT=support` so its JIDs become `telegram:support/<chat_id>`.
Routing rules select group by JID prefix (e.g. `telegram:support/*` → support group).

### Naming conventions

| Pattern                    | Example                                              |
| -------------------------- | ---------------------------------------------------- |
| `<adapter>-<label>.toml`   | `teled-support.toml`, `discd-gaming.toml`            |
| Port offset per instance   | primary: 9001, secondary: 9002, ...                  |
| Env var prefix per account | `TELEGRAM_SUPPORT_BOT_TOKEN`, `DISCORD_GAMING_TOKEN` |

### Routing

Inbound: each message carries the source JID (`telegram:support/chat_id`).
Routing rules map JID prefix → group folder as usual.

Outbound: replies go back through the originating channel adapter (tracked via
`routed_to` column). No change needed.

Proactive sends (agent-initiated): use MCP `send_message` targeting a specific
JID including the account ID, which the chanreg proxy resolves to the correct
adapter URL.

### Constraints

- Each adapter instance needs a distinct `LISTEN_ADDR` port.
- Each adapter instance needs its own `LISTEN_URL` (used for webhook registration).
- Ports must be unique across the compose network.
- No shared state between instances — each is fully independent.

## Implementation

Nothing to implement. Pattern works today:

1. Add `services/<adapter>-<label>.toml` with distinct port and credentials.
2. Add env vars to `.env`.
3. `arizuko generate <instance>` — new service appears in compose.
4. Set `CHANNEL_ACCOUNT=<label>` in the service TOML environment.
5. Add routing rule: `"<platform>:<label>/*" → <group_folder>` in `.env`.

## Future (if needed)

If account identity needs to be explicit in agent context (e.g. "you are
@support_bot"), add `CHANNEL_LABEL` env var passed into agent system prompt.
Not needed for routing correctness — defer until requested.
