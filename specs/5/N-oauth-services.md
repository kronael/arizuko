---
status: brainstorm
depends: [9-acl-unified]
relates-to:
  [1-auth-standalone, E-rest-mcp-bridge, A-mcp-everywhere, 5-uniform-mcp-rest]
---

# specs/6/H — third-party OAuth services as agent capabilities (brainstorm)

## Why this is a brainstorm

Operators are starting to ask for Gmail, GCal, Drive, Notion, Linear,
GitHub, Microsoft 365, Asana, Jira access from agents. The cheap
answer is "write a Gmail adapter." The right answer is harder: every
one of those services has the same OAuth-2-then-REST shape, the MCP
ecosystem already publishes servers for most of them, and arizuko
already owns 80% of the primitives — `auth/oauth.go` does Google /
GitHub / Discord flows, `secrets` table is folder/user-scoped,
`auth.Authorize` gates actions, the REST↔MCP bridge (spec 6/E) is
the planned dispatch layer.

This spec frames the design space before any code lands. Sibling
brainstorm 6/E argues the bridge belongs in the platform; this one
argues OAuth-mediated services are the first non-arizuko upstream
that bridge has to handle, and what changes in the auth/secrets/ACL
schema to support it.

## What it solves

Operators want their agents to read Gmail, file GitHub issues, update
Linear tickets, draft Notion pages — acting as the operator (or the
end user, in shared-folder products). Today that requires either
hand-rolled adapters per service or giving agents long-lived API
tokens stuffed into `.env`, neither of which scales past one operator.
The spec defines one OAuth + scope + bridge shape that any REST-shaped
service plugs into, so adding "Asana" costs a catalog file plus a
provider entry, not a new daemon.

## Generic OAuth-service shape

Every service in scope has the same five-step lifecycle:

1. **Discovery** — operator says "I want Gmail on folder `corp/eng/`."
   Platform exposes a connect URL.
2. **Consent** — user clicks, picks scopes from a catalog the
   integration declares, redirects to the provider.
3. **Token exchange** — callback to arizuko, exchange code → access
   token + refresh token + expiry + granted scopes.
4. **Persistence** — store {provider, subject, folder/user scope,
   access, refresh, expiry, granted_scopes}. Refresh on demand.
5. **Use** — agent calls an MCP tool (`gmail:messages.send`); bridge
   resolves the token from storage, attaches it as the auth header,
   dispatches the REST call, gates via `auth.Authorize`.

Inbound push (Gmail Pub/Sub, GitHub webhooks, Slack events) is a
separate concern handled by per-service adapters when the operator
opts in. Outbound MCP tools are the always-on surface; inbound is the
optional surface (see "Two surfaces" below).

The provider differences are all data: auth URL, token URL, refresh
flow shape, scope vocabulary, REST base URL, token-injection style
(`Authorization: Bearer`, `?access_token=`, custom header). All
mechanical, all catalog-driven.

## Reuse: existing arizuko pieces

| Piece                                    | Today                                                                                              | What this spec adds                                                                                                                                                               |
| ---------------------------------------- | -------------------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `auth/oauth.go`                          | Google / GitHub / Discord login (identity only, `openid email profile` / `read:user` / `identify`) | Extend to per-provider scope sets + token persistence beyond session cookies.                                                                                                     |
| `secrets` table (`0034-secrets`)         | `(scope_kind, scope_id, key)` plaintext (post-`0047`)                                              | Either widen schema with provider/subject/expiry columns or add a sibling `oauth_tokens` table. Open question — see below.                                                        |
| `auth.Authorize` + ACL (`9-acl-unified`) | `(principal, action, scope, params, predicate, effect)`                                            | Reuse verbatim. Actions become `gmail:messages.send`, scopes become `gmail:user@domain/label/INBOX`.                                                                              |
| REST↔MCP bridge (`6/E`)                  | Brainstorm only                                                                                    | This spec is the first concrete upstream catalog the bridge serves. Specs co-evolve.                                                                                              |
| Folder secrets binding                   | Folder-scoped key/value                                                                            | OAuth tokens land in the same scope namespace — `folder:corp/eng/` owns the Gmail connection; multiple users in the folder share or shadow it (open question).                    |
| `identities` + `identity_claims`         | Cross-channel user merge                                                                           | Gmail/Notion accounts could be claimed as additional `identity_claims` rows (`gmail:user@domain` as a sub-form), making "this Gmail belongs to this human" first-class. Optional. |

Net new code is bounded to: per-provider scope catalogs, token-refresh
worker, the bridge's auth-header injection, and the connect/disconnect
MCP+REST handlers. Everything else is reuse.

## Reuse: off-the-shelf MCP servers

The MCP ecosystem ships servers for most target services. arizuko
should mount them rather than hand-write REST clients per provider:

| Service               | Server                                                                                     | Maturity                                                     |
| --------------------- | ------------------------------------------------------------------------------------------ | ------------------------------------------------------------ |
| Google Workspace      | `aaronsb/google-workspace-mcp`, `j3k0/mcp-google-workspace`, `MarkusPfundstein/mcp-gsuite` | Multiple; multi-account, OAuth flows in-process.             |
| Notion                | `makenotion/notion-mcp-server` (official)                                                  | Official, API 2025-09-03.                                    |
| Microsoft 365 / Graph | `microsoft/mcp` (catalog of official MS servers)                                           | Official.                                                    |
| Linear                | `mcp.linear.app/mcp` (hosted, OAuth 2.1 + DCR)                                             | Official hosted, dynamic client registration.                |
| GitHub                | `github/github-mcp-server`, hosted `api.githubcopilot.com/mcp`                             | Official; hosted does NOT support DCR — needs static client. |
| Slack, Discord, etc.  | Community servers + arizuko's existing adapters                                            | Mixed; arizuko already has inbound adapters.                 |

Two integration shapes for off-the-shelf servers:

- **Hosted (remote)**: Linear's `mcp.linear.app/mcp`, GitHub Copilot's
  `api.githubcopilot.com/mcp`. arizuko proxies MCP tool calls to the
  upstream; OAuth lives there (or DCR negotiates inline). Cheapest;
  arizuko provides the auth handshake glue.
- **Local (containerised)**: Notion / Google Workspace community
  servers. arizuko runs them as sidecar containers in the per-folder
  pod, injects tokens from folder secrets at spawn, exposes their
  tools through the bridge.

The REST↔MCP bridge (6/E) is the third shape — hand-written catalog
for services where neither an off-the-shelf server exists nor is
maintained. Decision per-service, not per-platform.

Connection back to 6/E: "off-the-shelf MCP server as bridge" was
listed as one of three catalog-source options in 6/E. This spec
commits to it for any service that has a maintained MCP server, and
falls back to a hand-written catalog only when none exists.

## Two surfaces per integration

Every service integration has at most two surfaces. Most ship only the
second:

### Inbound channel (optional)

Some services push events: Gmail Pub/Sub, GitHub webhooks, Slack
events, Linear webhooks, Notion automations. When the operator wants
arizuko to react to those, it's a thin adapter daemon (same shape as
`slakd` / `teled` / `whapd`) — HTTP webhook receiver, signature
verification, normalise to internal message envelope, post to gated.
Skipped when the operator just wants the agent to act on the service
on demand. Inbound is opt-in per integration per folder.

### Outbound MCP tools (always)

The agent's actual surface. Per-user OAuth token unlocks a set of
MCP tools (`gmail:draft`, `gmail:send`, `gcal:events.create`, etc.).
Bridge or mounted MCP server dispatches; `auth.Authorize` gates each
call against the folder's ACL.

Same separation already lives in 6/E: edge adapters stay inbound-only,
bridge handles agent → platform. This spec extends it from "platform
adapters we built" to "third-party OAuth services we connect to."

## Per-user OAuth identity

Three storage shapes on the table:

1. **Reuse `secrets`** — store the token JSON blob under
   `(folder, "oauth:gmail")`. Cheapest. Loses structure: no way to
   query "all expired Gmail tokens", no refresh worker without
   parsing JSON. Folder-scoped only; per-user scoping needs a key
   like `oauth:gmail:user@domain`.
2. **New `oauth_tokens` table** —
   `(provider, subject, scope_kind, scope_id, access_enc, refresh_enc, expires_at, granted_scopes, created_at, updated_at)`.
   Proper indices, refresh worker queryable, expiry monitoring
   trivial. Tracks provider-subject (the Gmail address) separately
   from arizuko's scope_id (folder or user) — both bind to the
   token, neither defines it.
3. **Extend `identity_claims`** — the `sub` column already namespaces
   identities (`telegram:123`, `whatsapp:456`); add
   `gmail:user@domain` and `github:user` as sub-forms, with tokens
   stored alongside. Forces every OAuth grant to belong to a
   canonical arizuko identity — strong for cross-channel claim
   merging, awkward for service accounts (a folder-owned Gmail with
   no human identity).

The two-table answer (option 2) is leading because it doesn't conflate
"who is this human" with "what tokens does this folder/user hold."
`identities` stays the merge graph; `oauth_tokens` is the wallet.

Per-folder vs per-user binding: both legitimate. A solo operator's
folder probably has one Gmail account. A corp folder might have one
shared "ops@" account plus per-user personal accounts gated by ACL.
Schema must support both; `scope_kind` already does (`folder` |
`user`). Lookup order at call time: user-scope token first, fall back
to folder-scope. Same shape as `secrets` resolution.

## ACL gates

No new auth machinery. Reuse `auth.Authorize` from spec 9.

```
auth.Authorize(
  caller_folder,           // folder making the call
  "gmail:messages.send",   // action
  "gmail:atlas@example/*", // scope (account / label / file / repo glob)
  args,                    // for params/predicate
)
```

Action vocabulary follows 6/E's convention: `<provider>:<verb>`.
Provider-scoped verbs are free strings; documenting them is the
provider catalog's job. Scope grammar piggybacks on the JID-shaped
globs the ACL already matches.

Tier defaults (e.g. `tier:plus` gets `gmail:messages.read` but not
`gmail:messages.send` until explicitly granted) stay in
`grants.DeriveRules` (the same code path that handles `mcp:*` today).
Per-folder overrides are rows in `acl`. Per-message-account scoping
(e.g. "agent can send from ops@ but not from ceo@") falls out of the
scope glob naturally.

## Bigger vision: arizuko as agent-GSuite

GSuite became GSuite because it nailed five things at once:
multi-tenancy with strong tenant isolation, identity + SSO that
external apps could federate to, governance (Vault, DLP, audit,
context-aware access), billing with per-seat unit economics, and a
marketplace where third parties extended the platform without
forking it. None of those are AI-specific. All of them apply to a
multi-tenant agent runtime.

arizuko's bet — implicit in the current architecture, explicit if
this lands — is the same playbook for agents:

- **Tenants**: folders (already shipped). Hierarchical, isolated,
  ACL-gated. The agent-equivalent of a Workspace domain.
- **Identity**: `identities` + `identity_claims` (already shipped).
  Cross-channel sub merge today; cross-service OAuth merge with this
  spec. The agent-equivalent of Google sign-in.
- **Per-user credentials**: this spec. End users authorise agents to
  act on their behalf in third-party systems. The agent-equivalent of
  GSuite's "sign in with Google" federation, inverted — instead of
  the platform being the IdP everyone else trusts, arizuko is the
  client that holds tokens for every IdP the user has.
- **Governance**: ACL (spec 9), audit log (`specs/3/c`), HITL
  firewall (spec 7/4). Need to mature: structured per-tool-call
  audit, exportable to operator's compliance stack.
- **Billing**: cost-metering exists (spec `plan-metering`). Per-seat
  / per-token unit economics is the gap.
- **Marketplace**: products (spec 7), templates, skills. Today
  operators pick from a curated list; nothing third-party
  installable. Skills could be the unit — bundled, signed, scoped,
  reviewable.

The five GSuite primitives mapped to arizuko's current state:

| Primitive      | GSuite                                  | arizuko today                                 | Gap                                                        |
| -------------- | --------------------------------------- | --------------------------------------------- | ---------------------------------------------------------- |
| Tenant         | Workspace domain                        | Folder hierarchy                              | None — primitive scales corp/eng to solo/inbox.            |
| Identity / SSO | Sign in with Google → 3p apps           | `auth/oauth.go` (Google/GitHub/Discord login) | OAuth-as-client (this spec) for outbound service identity. |
| Capabilities   | Drive / Gmail / Calendar APIs           | MCP tools per agent                           | Third-party MCP capability mounting (this spec + 6/E).     |
| Governance     | Vault, DLP, audit, context-aware access | ACL + HITL queue + audit log                  | Compliance-grade audit export; data-residency story.       |
| Billing        | Per-seat unit economics + admin console | Cost metering (planned)                       | Per-seat / per-tier productisation; admin billing UI.      |
| Marketplace    | Workspace Marketplace                   | Curated `--product` list                      | Third-party skill/persona installation + signing.          |

"Agent-GSuite" is not a feature; it's the shape that drops out if the
five primitives stay orthogonal. Each one can ship independently. The
claim is defensible only when all five exist with the same minimality
arizuko already maintains for folders, channels, and ACL.

What's missing as platform primitives (none of which this spec
delivers — just names them):

- **Per-user agent state** — today agent state is folder-scoped
  (`.claude/` lives under the group). Multi-user folders need
  per-user memory partitions. Spec 7/35 (tenant self-service)
  touches this; needs a real primitive.
- **Multi-user shared agents** — one agent, many human principals
  acting through it, each with their own OAuth wallet and ACL view.
  Most of the primitives exist; the integration test doesn't.
- **Audit trails for tool calls** — `audit_log` exists but isn't
  structured per-tool-call yet; needed for compliance-grade exports.
- **Quota + billing primitives** — per-folder usage caps, per-tier
  pricing, downstream attribution to upstream API spend.
- **Marketplace** — third-party skill/persona installation, with
  signing, scoped permissions, review queue, and revocation.

These are the gaps between "useful agent runtime" and "agent-GSuite".

## Open questions

1. **Token storage**: extend `secrets` (cheap, ugly) or new
   `oauth_tokens` table (clean, more SQL)? Leaning new table.
2. **Folder-vs-user binding**: lookup precedence when both exist for
   the same provider? User-first, then folder fallback feels right;
   confirm against actual operator use cases.
3. **Refresh worker**: where does it run? Standalone `oauthd`,
   folded into gated, or lazy refresh on first 401? Lazy is
   simplest, refresh worker is more predictable for long-lived
   agents.
4. **Hosted vs local MCP server choice**: do we let operators pick
   per-folder, or platform-decide based on which provider supports
   what? Linear hosted is great; GitHub hosted lacks DCR; Google
   ecosystem is mostly local servers. Heterogeneity is irreducible.
5. **DCR vs static client**: arizuko-as-MCP-client must handle both
   (Linear supports DCR; GitHub Copilot's hosted MCP does not).
   Capability detection at mount time or per-provider config?
6. **Catalog vs MCP server fallback**: clear rule for when to write
   a bridge catalog entry vs mount an off-the-shelf server. Default
   to off-the-shelf; bridge only when the server is absent,
   abandoned, or scope-creeping into unrelated surface.
7. **Scope semantics for shared accounts**: how does the platform
   express "agent can act as ops@ but not as ceo@" in scope strings?
   `gmail:ops@example/*` works; "agent can read INBOX but not send"
   needs action + scope combinations the ACL supports today, but the
   UX of writing those rules in a dashboard is non-trivial.
8. **Token revocation on disconnect**: when an operator disconnects
   a service, should arizuko revoke the OAuth token upstream, or
   just delete the local copy? Upstream revoke is the right answer
   for trust; some providers don't expose revoke endpoints.
9. **Audit granularity**: every bridged tool call goes through
   `auth.Authorize` — that's a natural audit point. Does the audit
   row include the request body, redacted, full? Compliance-shaped
   question; not for v1.
10. **Service account vs per-user**: some integrations want a
    folder-owned service account (no human identity behind it).
    `oauth_tokens` shape needs to express this; `identity_claims`
    integration doesn't apply. Probably: `scope_kind = folder`
    binding with no claim row, vs `scope_kind = user` with optional
    claim merge.

## What this is NOT (v1)

- NOT a Gmail-specific spec. Gmail is the first stress test; the
  shape must serve Notion, Linear, GitHub, MS Graph, Asana, Jira
  without per-service spec rewrites.
- NOT a new auth daemon. Reuse `auth/` library + REST↔MCP bridge.
- NOT an MCP server rewrite. Mount off-the-shelf where they exist;
  bridge-catalog where they don't.
- NOT a marketplace. Skill/persona signing + third-party install is
  named as a future primitive, not specced here.
- NOT a compliance / DLP product. Audit + per-tool gating is the
  v1 ceiling; data-residency / DLP / Vault-equivalent are vision,
  not scope.
- NOT a UI spec. Connect / disconnect flows need a dashd surface;
  that's downstream.
- NOT a rewrite of edge adapters. Slakd, teled, whapd stay as
  inbound channels; this spec covers outbound capability mounting
  only.

## Next steps

1. Land 6/E (REST↔MCP bridge) at least to "spec" status — this spec
   depends on it for dispatch.
2. Prototype token storage (option 2: `oauth_tokens` table) against
   Google as the first provider since `auth/oauth.go` already does
   the auth-code half.
3. Mount Linear's hosted MCP (`mcp.linear.app/mcp`) as the first
   third-party server end-to-end. DCR-capable hosted MCP is the
   simplest integration shape; proves the OAuth-store-token-mount
   loop before tackling local servers + complex providers.
4. Promote to `spec` once token shape + bridge integration are
   confirmed; product catalog (which services ship in v1) is a
   separate decision tracked in specs/7.
