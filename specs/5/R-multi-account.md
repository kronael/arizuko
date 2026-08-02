---
status: shipped
---

# Multi-account channels

Several accounts on one platform = several service instances of the same
adapter binary, each with its own credentials. **No new code** — the
mechanism is compose packaging plus the existing registry.

Each account gets its own service block in the adapter's compose
fragment (`template/services/<daemon>.yml`, `include`d verbatim per
[`27-compose-native-packaging.md`](27-compose-native-packaging.md)) with
a distinct `container_name`, `env_file`, `LISTEN_URL`, and a distinct
`CHANNEL_NAME`. `LISTEN_ADDR` stays `:8080` — docker network
namespacing makes per-container ports collision-free.

**`CHANNEL_NAME` is what distinguishes accounts, not the auth
principal.** Every instance of one adapter exchanges the same
`service:<daemon>` token (`AUTHD_SERVICE_NAME`, e.g. `teled`), so routd
cannot bind a registration name to a token subject — which is exactly
why `channelAuth` admits any seeded service principal and leans on the
origin pin instead
([`34-channel-protocol.md`](34-channel-protocol.md)).

**Return-adapter selection is by `messages.source`, not by JID.** When
two instances share a JID prefix (`telegram:`), prefix lookup alone is
ambiguous — `chanreg.ForJID` returns whichever it finds first. The
inbound stamps `messages.source` with the receiving instance's
`CHANNEL_NAME`, and outbound resolves through that
(`34-channel-protocol.md` § Outbound and adapter resolution). Registering
narrower, per-account `jid_prefixes` is the alternative when an adapter's
platform gives it an account segment to key on
([`S-jid-format.md`](S-jid-format.md)); today's adapters register the
broad platform prefix and rely on `source`.

Nothing tells the agent which account it is on. If that becomes needed,
it belongs in the prompt envelope, not in the JID.
