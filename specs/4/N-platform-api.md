---
status: spec
depends:
  [
    ../1/W-slink.md,
    ../6/5-uniform-mcp-rest.md,
    ../6/R-platform-api.md,
    ../5/32-tenant-self-service.md,
  ]
---

# Platform API — group lifecycle + message submission, one surface

arizuko already exposes two halves of a platform: [slink](../1/W-slink.md)
(submit a message into a running group, observe rounds) and the
group-setup machinery driven by `container.SetupGroup` +
`store.PutGroup`. The first is a public HTTP protocol; the second is
reachable only from the CLI ([`cmd/arizuko/main.go:229`](../../cmd/arizuko/main.go)),
onbod invite-redemption ([`onbod/main.go:483`](../../onbod/main.go)),
or the MCP tool `register_group` ([`ipc/ipc.go:986`](../../ipc/ipc.go)).
No HTTP caller can go from "I want a new agent" to "here is the chat
URL" in one round-trip.

The platform API closes the gap. One base URL, one auth model, one
contract — CRUD over groups plus the existing slink surface — so an
operator (or another agent) can spawn, configure, post to, observe,
and tear down an arizuko agent without invoking the CLI.

## Naming

"Platform API," not "MSP." [SMTP submission (RFC 6409)](https://www.rfc-editor.org/rfc/rfc6409)
inspires the message-submission half — a narrow protocol that hands a
message to an authenticated relay — but the group-lifecycle half is
broader than message submission, and a Matrix-style "client-server API"
overreaches (no client SDK contract here, only an HTTP surface). The
slink subset already carries the submission semantics
([W-slink.md](../1/W-slink.md)); the new surface IS the platform.

## Surface choice

Mounted on **gated**'s `/v1/*` namespace alongside the resources from
[`6/R-platform-api.md`](../6/R-platform-api.md). gated owns the
backing tables (`groups`, `routes`, `user_groups`, `secrets`) and
holds `SetupGroup` in-process ([`gateway/gateway.go:155`](../../gateway/gateway.go))
— any sibling daemon would re-wire it over HTTP. webd's `/slink/*` is
the public unauthenticated guest surface ([Z-slink-widget.md](../1/Z-slink-widget.md));
authenticated control operations don't fit there.

MCP face is automatic per [`6/5-uniform-mcp-rest.md`](../6/5-uniform-mcp-rest.md):
each `/v1/groups` action registers as an MCP tool through the same
`Resource{}`. Agents call `groups.create` over MCP; operators call
`POST /v1/groups` over HTTPS. One handler, two faces.

## Endpoints

The platform API is the union of the group-CRUD resource and the
slink message-submission resource. Slink rows are inherited verbatim
from [W-slink.md](../1/W-slink.md); group rows are new.

| Method | Path                           | Action            | Surface    | Source                                                        |
| ------ | ------------------------------ | ----------------- | ---------- | ------------------------------------------------------------- |
| POST   | `/v1/groups`                   | `groups.create`   | gated, MCP | new                                                           |
| GET    | `/v1/groups`                   | `groups.list`     | gated, MCP | new                                                           |
| GET    | `/v1/groups/{folder}`          | `groups.get`      | gated, MCP | new                                                           |
| PATCH  | `/v1/groups/{folder}`          | `groups.update`   | gated, MCP | new (rename, change parent, persona)                          |
| DELETE | `/v1/groups/{folder}`          | `groups.delete`   | gated, MCP | new                                                           |
| POST   | `/v1/groups/{folder}/grants`   | `grants.create`   | gated, MCP | [6/R-platform-api.md](../6/R-platform-api.md)                 |
| POST   | `/v1/groups/{folder}/secrets`  | `secrets.put`     | gated, MCP | [5/32-tenant-self-service.md](../5/32-tenant-self-service.md) |
| POST   | `/slink/{token}`               | `messages.submit` | webd, MCP  | [W-slink.md](../1/W-slink.md)                                 |
| GET    | `/slink/{token}/{turn_id}`     | `rounds.get`      | webd, MCP  | [W-slink.md](../1/W-slink.md)                                 |
| GET    | `/slink/{token}/{turn_id}/sse` | `rounds.stream`   | webd       | [W-slink.md](../1/W-slink.md)                                 |

The two namespaces stay split by daemon ownership: gated owns the
folder, webd owns the inbound chat surface. Both sit behind the same
proxyd at the same `WEB_HOST`. `groups.create` returns the slink URL
pre-composed, so the caller never sees the split.

## Create

`POST /v1/groups` body:

```json
{
  "folder": "atlas/support",
  "name": "Support",
  "parent": "atlas",
  "prototype": "atlas/prototype",
  "product": "slack-team"
}
```

Server actions, in order: scope check (`auth.MatchesFolder`), folder
validation (`groupfolder.IsValidFolder`), `store.PutGroup` with a
freshly-minted `slink_token` (per [W-slink.md](../1/W-slink.md), never
rotated), `container.SetupGroup` ([`container/runner.go:763`](../../container/runner.go))
to seed `.claude/` + skills, then `store.SeedDefaultTasks`. Same
sequence as `arizuko group add` ([`cmd/arizuko/main.go:288`](../../cmd/arizuko/main.go)).

Response:

```json
{
  "folder": "atlas/support",
  "name": "Support",
  "parent": "atlas",
  "slink_token": "646a...",
  "slink_url": "https://example.com/slink/646a.../chat",
  "mcp_url": "https://example.com/slink/646a.../mcp",
  "created_at": "..."
}
```

One round-trip from intent to live agent. The slink URL composition
mirrors [Z-slink-widget.md](../1/Z-slink-widget.md) `/config` — the
caller never builds it.

## Read / update / list

- `GET /v1/groups/{folder}` returns the row plus the slink config
  (token, urls, name).
- `GET /v1/groups?parent=atlas` lists children; `?glob=atlas/**` filters
  by the same patterns ACL uses ([`auth.MatchGroups`](../../auth/acl.go)).
- `PATCH /v1/groups/{folder}` — rename (`name`), change persona
  (`product`). Reparent requires a delete + create today; v2.

## Delete

`DELETE /v1/groups/{folder}` is asynchronous and idempotent:

1. Stop accepting inbound for `folder` (mark `groups.deleted_at`).
2. Wait for in-flight round (`turn_results` row pending for the folder)
   to land or hit timeout — gateway already gates spawns per folder.
3. Remove `groups`, `routes`, `user_groups`, `grants`, `secrets` rows
   for `folder`. Preserve `messages` and `turn_results` for audit.
4. Leave `groups/{folder}/` on disk; operator-driven prune is a separate
   verb. Container instances die when their lease expires.

Response: `202 Accepted` with `{deleted_at, pending_rounds: N}`. The
caller polls `GET /v1/groups/{folder}` for `404` to confirm cleanup.

## Auth and grants

One auth model: the [`6/R-platform-api.md` "Token model"](../6/R-platform-api.md)
JWT, verified by [`auth/middleware.go`](../../auth/middleware.go)
`RequireSigned`. No new scheme. The platform API just declares which
scopes gate which actions, citing [GRANTS.md](../../GRANTS.md):

| Scope                    | Effect                                          | Typical holder                                                   |
| ------------------------ | ----------------------------------------------- | ---------------------------------------------------------------- |
| `groups:write`           | create / delete any folder                      | operator (`user_groups` row `**`)                                |
| `groups:write:own_group` | create / delete under `Identity.Folder` subtree | agent at tier ≥ 2 ([GRANTS.md "tier defaults"](../../GRANTS.md)) |
| `groups:read`            | list / get any folder                           | operator, dashd                                                  |
| `groups:read:own_group`  | list / get under subtree                        | agent, user                                                      |
| `messages:submit`        | POST to any slink                               | anon (rate-limited per W-slink) or holder of the token           |

Example: an agent in `atlas/support` (tier 2) holds
`groups:write:own_group`. It can `POST /v1/groups {folder: "atlas/support/launch-q3"}`
— scope check passes (target is in subtree). It cannot
`POST /v1/groups {folder: "rhias/main"}` — 403. The same agent calls
`groups.create` over its MCP socket; the handler is the same.

## Multi-tenant shape

Same endpoints serve `solo/inbox` and `corp/eng/sre/oncall/launch-q3`,
per CLAUDE.md "simple stays simple, complex goes deeper." A single-user
instance creates one root group at install and never calls `POST /v1/groups`
again. A multi-tenant deployment calls it per onboarding event, with
`parent` carrying the org subtree. Existing invite-redemption
([5/32-tenant-self-service.md](../5/32-tenant-self-service.md)) and
`spawnFromPrototype` ([gateway/gateway.go:172](../../gateway/gateway.go))
become internal clients of the same endpoint.

## What's in scope, what's not

In scope:

- Group CRUD over HTTP and MCP, one handler.
- `slink_token` in the create response — composes with W-slink.
- Grants and secrets verbs scoped to a folder (POST-only, idempotent on
  key).

Out of scope:

- Round-done webhooks to external systems (covered by webd's
  `/v1/round_done` channel-adapter callback; external delivery is a
  separate adapter).
- Billing, quota, multi-region — none exist in arizuko today.
- A client SDK. The JS slink SDK ([Z2-slink-sdk.md](../1/Z2-slink-sdk.md))
  covers the submission half; a group-management SDK ships only when a
  second consumer appears.
- Streaming group-state changes (group.created / group.deleted as SSE).
  Polling `GET /v1/groups` is the v1 contract.

## Acceptance criteria

1. `POST /v1/groups` with operator token + valid body returns 201,
   `slink_token` in response, `slink_url` resolves and `GET` returns
   the chat page (200).
2. `POST /v1/groups` with agent token whose `Identity.Folder=atlas/support`
   and body `{folder: "atlas/support/q3"}` returns 201; same agent
   targeting `rhias/main` returns 403.
3. `POST /v1/groups` is idempotent on folder: re-POSTing the same
   folder with the same body returns 200 (not 201), same `slink_token`.
4. `GET /v1/groups/{folder}` after create returns the row including
   `slink_url` and `mcp_url`; `GET /v1/groups?parent=atlas` lists
   exactly the subtree.
5. `DELETE /v1/groups/{folder}` returns 202 immediately; subsequent
   `GET` returns 404 after the in-flight round (if any) lands.
6. MCP `tools/list` over the group socket includes `groups.create`,
   `groups.list`, `groups.get`, `groups.update`, `groups.delete` — the
   same handler set, surfaced via the registry in
   [`6/5-uniform-mcp-rest.md`](../6/5-uniform-mcp-rest.md).
7. End-to-end: a fresh JWT issued at OAuth login can create a group,
   POST a message to the returned `slink_url`, stream the round's SSE,
   and delete the group — without any CLI step.

## What changes

| Package / file        | Change                                                                                         |
| --------------------- | ---------------------------------------------------------------------------------------------- |
| `gated/v1_groups.go`  | New: `Resource{Name:"groups",...}` + handler, registered per [6/5](../6/5-uniform-mcp-rest.md) |
| `gated/main.go`       | Wire `RegisterResource` call                                                                   |
| `gateway/gateway.go`  | Expose `spawnFromPrototype` + `SetupGroup` to handler; no behavior change                      |
| `auth/policy.go`      | Add `groups:write:own_group` scope predicate                                                   |
| `onbod/main.go`       | `handleCreateWorld` reduces to a `POST /v1/groups` client (same daemon, in-process call OK)    |
| `cmd/arizuko/main.go` | `arizuko group add` keeps its direct call; CLI is privileged                                   |
| `specs/4/index.md`    | Add row for this spec (spec section)                                                           |
| `webd/`               | No change — slink is already the message-submission half                                       |

## Open questions

- **Idempotency key vs folder.** Folder is the natural key; a
  `client_token` header for retry safety can land later.
- **Sync vs async create.** `SetupGroup` is fast (≤ 100 ms warm);
  inline is fine. Revisit with `202 + jobs` if skill-seeding grows.
- **Reparenting.** `PATCH {parent:...}` requires moving the folder
  on disk + rewriting child rows; not v1.
- **Bulk create.** Inherit [6/R-platform-api.md](../6/R-platform-api.md):
  many POSTs, bulk on demand.

## Reconciliations

- **vs [6/R-platform-api.md](../6/R-platform-api.md)** — that spec
  declares the federated surface + token model; this spec is the
  first concrete resource definition and brings W-slink under the
  same roof.
- **vs [6/5-uniform-mcp-rest.md](../6/5-uniform-mcp-rest.md)** — that
  spec defines the `Resource{}` registry; this spec is one
  registration. No new mechanism.
- **vs [W-slink.md](../1/W-slink.md)** — unchanged. Only addition:
  `POST /v1/groups` returns the slink URL pre-composed.
- **vs [5/32-tenant-self-service.md](../5/32-tenant-self-service.md)** —
  onbod's invite + create-world path becomes the first internal
  caller of `POST /v1/groups`; the public-facing endpoint is its sibling.
