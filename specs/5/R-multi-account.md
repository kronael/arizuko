---
status: shipped
---

# Multi-account channels

Multiple accounts per adapter type = multiple service instances with
different credentials. Compose generator already supports arbitrary
`services/*.toml`. Pattern: `services/<adapter>-<label>.toml` with
distinct `LISTEN_ADDR` + `LISTEN_URL` + per-account env vars. JIDs
become `platform:account/id` (see [S-jid-format.md](S-jid-format.md)).

No new code. Routing rules select group by JID prefix.

Future: if agent needs to know which account it's on, add
`CHANNEL_LABEL` env var into system prompt.
