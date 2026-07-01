---
status: active
supersedes: 5/5-uniform-mcp-rest
depends:
  [1-auth-standalone, 5/E-routd, 36-yaml-manifests, specs/4/9-acl-unified]
---

# specs/5/45 — one REST surface: author REST, annotate the OpenAPI

> Every cold-tier management resource is authored **once** as a REST
> handler; its `/openapi.json` is **annotated** (`x-mcp-*`: sharp name,
> when-to-use) and **published**. The agent uses the REST endpoints
> **directly**, guided by that doc — **no per-resource MCP tool, no
> gateway, no `deriveMCPTools`**. Dashboard and agent hit the same REST
> surface; one `auth.Authorize` gate, two identity sources (OAuth-gated
> browser, folder-scoped service token for the agent). Hot-tier agent
> actions (`reply`, `send`, `inspect_*`) stay MCP-only — no REST resource
> to mirror. Supersedes [`5/5`](5-uniform-mcp-rest.md);
> [`5/44`](44-mcp-rest-unification.md) is the rollout.

## The model

Two tiers, by design — not a pending migration:

- **Cold-tier (operator config)** — `routes`, `acl`, `groups`,
  `secrets`, `scheduled_tasks`, `network_rules`, `web_routes`,
  `route_tokens`, `onboarding_gates`, `proxyd_routes`. ONE REST handler
  per `(resource, action)`, reached over **REST** by both the human
  (OAuth-gated browser) and the agent (folder-scoped service token + the
  published annotated OpenAPI as its catalog). No per-resource MCP tool —
  the annotated `/openapi.json` IS the agent's catalog.
- **Hot-tier (agent runtime)** — `reply`, `send`, `like`, `delete`,
  `post`, `diary`, session control (`fork_topic`, `engage`, `disengage`,
  `reset_session`, `inject_message`), inspect (`inspect_routing`,
  `inspect_tasks`, `inspect_session`). **MCP-only by design**, authored
  in `ipc/ipc.go`. These are agent-to-conversation primitives; an
  operator REST mirror adds nothing (operators don't `reply` to chats).
  NEVER fold them into the cold-tier interop.

The only thing that differs between the two cold-tier faces is the
identity source. The gate is the same.

## The annotation vocabulary (`x-mcp-*`)

OpenAPI 3.1 allows vendor extensions (`x-*`) on any object. `resreg`
folds the resource's `MCPDoc` into the emitted `openapi.json` as the
operation `description` + `x-mcp-when` (commit `52f92004`). The agent
reads these as its catalog; tooling that ignores them sees a normal
OpenAPI doc.

| Field          | On        | Meaning                                                                            |
| -------------- | --------- | ---------------------------------------------------------------------------------- |
| `x-mcp-desc`   | operation | The load-bearing "when to use this" prose. Emitted as the operation `description`. |
| `x-mcp-when`   | operation | Sharp trigger for the agent — when to reach for this endpoint over another.        |
| `x-mcp-hidden` | operation | `true` → keep the operation out of the agent's catalog (internal control plane).   |

`x-mcp-desc` is strict-on-purpose: an operation with no agent-facing
description is garbage to the model. This mirrors arizuko's "strict, not
magical" rule — no silent fallback to the SDK-facing `summary`.

## Auth model — one gate, two identity sources

Both surfaces produce a surface-agnostic `Caller` consumed identically,
and both run the SAME gate: `auth.Authorize` over the unified ACL row
table ([4/9](../4/9-acl-unified.md)). It composes two checks:

1. **Scope / ACL** — "may this principal perform this action at this
   scope". The canonical decision.
2. **Folder containment** — for a subtree-bound resource, the caller's
   folder must own the target's folder (target is the caller's folder or
   a descendant). Empty caller folder = root / service token =
   unrestricted (adapters and `service:routd` legitimately span folders).

The two surfaces differ only in the IDENTITY SOURCE feeding the lens:

| Surface | Identity carrier                                              | Verifier          | Folder source                        |
| ------- | ------------------------------------------------------------- | ----------------- | ------------------------------------ |
| Browser | `Authorization: Bearer <jwt>` (OAuth session, proxyd-stamped) | `auth.VerifyHTTP` | JWT `arz/folder` → `Extra["folder"]` |
| Agent   | folder-scoped service token calling `/v1/*` directly          | `auth.VerifyHTTP` | token's `arz/folder`                 |

A platform token is an ES256 JWT signed by `authd` — the **sole signer**
([1-auth-standalone.md](1-auth-standalone.md)). Every daemon verifies
offline against `authd`'s JWKs (`/v1/keys`); none mint their own. The
token carries `sub`, `scope`, and the namespaced folder claim
`arz/folder` (`auth/` treats it opaquely, surfaced via
`Identity.Extra["folder"]`; the folder-match helper lives in arizuko's
`identity.go`, keeping `auth/` folder-agnostic). Folder bounds the
subtree; scopes bound the verbs. There is no `tier` — authorization is
scope-match (capability tokens via authd downscope).

Scope vocabulary: `<resource>:<verb>[:own_group]`. `:own_group` scopes to
the caller's folder subtree. `<resource>:*` covers all verbs on a
resource; there is no `*:*` global wildcard — operators carry the
enumerated list. The gate is `auth.Authorize` over the ACL rows; the
scope string is the operator-token-minting shorthand over those rows,
not a second authorization path.

**Containment binds per resource to its folder-bearing param:** a `jid`
resolves to its routing-target folder before the check; a `run_id`
resolves to its run's folder (a cross-folder run reads as absent, 404,
not leaked); a bare `folder` param is contained directly. `POST
/v1/messages` stays cross-folder by design (one adapter routes many
folders); cost reporting uses a dedicated `cost:write` scope plus folder
containment. A folder-scoped token MUST NOT read or act cross-folder.

## One owner per table; federation over HTTP

Each table lives in exactly one daemon's DB; that daemon serves its
`/v1/*` face. Cross-daemon calls are **HTTP forwards** carrying the
caller's capability token — the owner is resolved by compose service
naming (`<DAEMON>_URL`, e.g. `PROXYD_URL=http://proxyd:8080`), NEVER a
lookup registry (identity is configured, not derived — CLAUDE.md).

| Daemon     | Owns / serves                                                                                                      |
| ---------- | ------------------------------------------------------------------------------------------------------------------ |
| **routd**  | groups, routes, messages, sessions, channels, web_routes, route_tokens, grants, acl, secrets, network_rules, tasks |
| **runed**  | spawns, run history — control-plane REST only (`/v1/runs`), no resreg resource (see below)                         |
| **authd**  | signing keys, JWKs, sessions — `/v1/tokens`, `/v1/keys`, `/auth/*` login (sole signer)                             |
| **onbod**  | invites, admissions, auth_users, onboarding_gates                                                                  |
| **proxyd** | proxyd_routes (`/v1/proxyd_routes`); login delegates to authd                                                      |
| **dashd**  | nothing — FS-mounted, reads routd.db directly for display; writes via HTTP to owners                               |

`proxyd_routes` (proxyd's reverse-proxy table) and `routes` (routd's
message-routing table) are two distinct resources with distinct names —
never conflate them (renamed `aab3487a`).

## Resource name = wire identity

The resreg `Name` becomes `/v1/<name>` AND the OpenAPI `operationId`
prefix (`<name>.<action>`). Two daemons NEVER share a name — the name is
globally unique wire identity. The composed `<name>.<action>` string is
the operator-facing contract: OpenAPI `operationId`, audit-log `action=`
fields, metrics labels, permission-editor rows. URL renames and
handler-function renames don't break it.

## Audit contract

Every state-changing handler MUST write exactly one `audit_log` row in
the SAME transaction as the mutation; if the audit write fails, the
mutation rolls back. Read-only handlers emit slog only — no audit row.
Field shape: `caller=<sub> resource=<name> action=<verb>
surface=rest target=<folder> result=<allowed|denied|error>`. `action`
is the stable correlator — one `grep 'resource=groups action=create'`
returns work whether it arrived via `POST /v1/groups` (browser) or
`arizuko group add`. Forwarders (`Store == nil`) skip the row; the
downstream daemon writes it (no double-log). Field schema:
[`I-tool-call-logging.md`](I-tool-call-logging.md).

## Anti-patterns — what should NOT be a resource

Same shape each: hot path, high-volume internal event, or stream rather
than CRUD verb. All carry `x-mcp-hidden: true` (REST stays) or aren't
resources at all:

- **Inbound message ingestion** — per-message hot path; the poll loops
  write `messages` rows directly. (`inject_message` for synthetic sends
  IS an MCP tool — audited, low-volume.)
- **Cost-log writes** — per-Claude-call; direct store write from adapters
  and `timed`. Not an operator action.
- **Streaming surfaces** — slink stream, agent live output. Not CRUD/RPC;
  SSE sits next to resreg ([J-sse.md](J-sse.md)), never inside it.
- **Auth session creation** — minted by `authd`, verified by `auth/`.
  Substrate, not a user tool.
- **Migrations** — file-driven (`*/migrations/`), run by the owning
  daemon at startup. Not a resource.
- **runed run-control** (`POST /v1/runs`, `/v1/runs/stop`,
  `GET`/`DELETE /v1/runs/{id}`) — REST-only BY DESIGN: the internal
  routd→runed execution control plane. Agents never spawn or kill runs.
  runed exposes no resreg resource (`OpenAPIHandler("runed", []string{})`
  is empty by design — control verbs, not CRUD). The absence is correct,
  not a uniformity gap.

The rule: user-initiated, audit-worthy, allow/deny-shaped → resreg.
High-rate side effect of normal operation → not.

## Orthogonal specs (don't fold in)

- [`5/44`](44-mcp-rest-unification.md) — the **adoption program** that
  rolls this model out resource-by-resource (pilot `routes`, then
  replicate). 44 = roll it out; 45 = the mechanism. Migration steps live
  in 44, not here.
- [`5/36`](36-yaml-manifests.md) — the **same cold-tier resources as
  declarative YAML** you `export`/`apply` (config-as-data). A different
  front on the same tables; the row-level CRUD engine underneath both is
  shared plumbing, not a concept named here.
- [`11/17-mcp-firewall.md`](../11/17-mcp-firewall.md) — per-call
  allow/deny **filtering** between agent and MCP server. Gates the
  hot-tier MCP surface; cold-tier goes over REST, not MCP.

## Acceptance

- A cold-tier resource is served by exactly one REST handler; its
  `/v1/<res>` endpoint and its OpenAPI entry both derive from it — no
  hand-authored `MCPTools` list.
- `GET /openapi.json` carries the annotated `description` + `x-mcp-when`
  for each non-hidden operation; the published doc is the agent's catalog.
- An `x-mcp-hidden: true` operation is absent from the published catalog
  but reachable via its REST path.
- Auth parity: a token with `grants:write:own_group` and folder
  `atlas/support` can `PATCH /v1/grants/{id}` under `atlas/support/*`;
  the same token cannot touch a grant under `rhias/*` (403) — whether the
  caller is the browser or the agent's service token.
- Hot-tier tools (`reply`, `send`, `inspect_*`) have no REST twin and no
  OpenAPI entry — MCP-only, by design.

## Open — agent→REST auth

The one unbuilt gap: the **folder-scoped service token** the agent uses
to call internal `/v1/*` directly (guided by the published OpenAPI). The
per-turn container gets a scoped MCP socket today; it needs the
equivalent folder-scoped HTTP credential to reach the cold-tier REST
surface (over `<DAEMON>_URL`, verified by `auth.VerifyHTTP`). This is the
prerequisite for retiring the interim cold-tier MCP tools — until it
lands, cold-tier stays reachable over the agent socket.

Deferred: short token TTL (1h) + a revocation table for long-lived
dashd-issued keys; pagination harmonization (MCP arrays vs REST cursor
pagination) left per-resource for now.
