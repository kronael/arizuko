---
status: draft
depends: [Y-secret-broker, 11/14-surrogate-oauth]
relates-to: [4/9-acl-unified]
---

# specs/5/N — third-party OAuth services as agent capabilities

## What this is

Letting an arizuko agent use a third-party service (GitHub, Linear,
Notion, Gmail, …) **without a per-service adapter**. The mechanism
already exists; this spec is the index that names where, plus the two
decisions not yet owned elsewhere.

## The mechanism already ships

Verified against code (2026-05-28):

- **Dispatch** — `ipc/connector.go` mounts a third-party MCP server as
  a stdio subprocess, renders `{secret:KEY}` env from the secrets
  table, proxies `tools/call`, scrubs secret values from results, gates
  via `auth.Authorize("mcp:"+name)`. Loaded from `connectors.toml`
  (`gateway/connectors.go`). This is the "REST↔MCP bridge" 5/N
  originally posited as undesigned — it is the **mounted-MCP-server**
  shape and it is built.
- **Secret storage** — the `secrets` table `(scope_kind, scope_id,
key)` already holds tokens. [`11/14`](../11/14-surrogate-oauth.md)
  adds the 4 additive columns for OAuth (refresh, expiry, provider,
  granted scopes). No new `oauth_tokens` table.
- **Token writer (the OAuth dance)** — [`11/14`](../11/14-surrogate-oauth.md):
  "Connect GitHub" in dashd → OAuth flow → access + refresh land in
  the `secrets` table the broker reads. `auth/oauth.go` today is
  identity-only (`exchangeGoogle` discards refresh/expiry,
  `auth/oauth.go:269`); 11/14's `auth/surrogate.go` is the unbuilt
  token-persisting half.
- **Broker** — [`6/Y`](Y-secret-broker.md): M2–M6 shipped (schema,
  dashd UI, CLI, spawn-env, connector spawner). **M0/M1 unshipped** —
  `ipc/ipc.go:845` passes `nil` secrets to `CallConnectorTool`; the
  broker middleware that resolves `user∥folder` secrets and passes
  them through is ~110 LOC not yet written.
- **ACL** — `auth.Authorize` gates each connector tool call. Note:
  tier-default fallback in `grants.DeriveRules` fires only for `mcp:`-
  prefixed actions (`auth/authorize.go:102`); connector tools already
  carry the `mcp:` prefix, so this works as-is.

## Minimal v1

GitHub via a **pasted PAT** (no OAuth dance yet):

1. Operator pastes a GitHub PAT into a `secrets` row (`/dash/me/secrets`
   already exists).
2. `connectors.toml` declares the GitHub MCP server with
   `{secret:GITHUB_PAT}` in its env.
3. Ship **6/Y M0/M1** — the broker middleware that resolves the secret
   and passes it to `CallConnectorTool` instead of `nil`. ~110 LOC.

That's the whole vertical slice. No new daemon, no new table, no
bridge, no OAuth flow. The agent calls the GitHub MCP server's tools,
gated by `auth.Authorize`, with the PAT injected by the broker.

OAuth (replace pasted PAT with a real "Connect" flow) is
[`11/14`](../11/14-surrogate-oauth.md), shipped after M0/M1.

## What this spec still owns

Two decisions not covered by 6/Y or 11/14:

1. **Hosted vs local MCP server, per provider.** Linear ships a hosted
   MCP (`mcp.linear.app/mcp`, OAuth 2.1 + DCR); GitHub's hosted lacks
   DCR; Google is mostly local community servers. Does the operator
   pick per-folder, or does the platform decide by provider capability?
   Heterogeneity is irreducible — `connectors.toml` handles local
   stdio servers today; hosted-remote needs a proxy mode.
2. **Catalog vs off-the-shelf fallback.** Rule for when to mount an
   existing MCP server vs hand-write a connector. Default: mount
   off-the-shelf; hand-write only when the server is absent, abandoned,
   or scope-creeping.

Everything else 5/N previously discussed (token storage shapes,
agent-GSuite vision, the OAuth lifecycle, ACL scope grammar) is owned
by 6/Y, 11/14, and 4/9. This spec does not duplicate them.

## Pointers

- [`6/Y-secret-broker.md`](Y-secret-broker.md) — the broker; ship M0/M1.
- [`11/14-surrogate-oauth.md`](../11/14-surrogate-oauth.md) — the OAuth token writer.
- `ipc/connector.go` — the dispatch path (mounted MCP server).
- `gateway/connectors.go` — `connectors.toml` loader.
- `ipc/ipc.go:845` — the `nil`-secrets gap M0/M1 closes.
