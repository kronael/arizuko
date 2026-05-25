---
status: draft
depends: [5-uniform-mcp-rest, 9-acl-unified, 35-proxyd-standalone]
---

# MCP-everywhere for platform management

Every state-changing operation on the platform is reachable via an MCP
tool (and the symmetric `/v1/*` REST endpoint), through one handler in
the `resreg` registry (`resreg/README.md`, `specs/5/5-uniform-mcp-rest.md`).
The CLI becomes a thin client; dashd becomes a thin server; direct SQL
writes outside `store/` are deleted.

The first instance — proxyd's runtime route table — shipped in
v0.36.0 (`specs/6/index.md` table row for 2-proxyd-standalone). This
spec catalogs the remaining gaps and the rollout shape.

## The principle

One handler per `(Resource, Action)`. Three call-sites for that
handler: REST (operator over OAuth), MCP (agent over capability
token), CLI (operator over a local socket). All converge on the same
function in `resreg.Resource.Handler`. No surface is privileged with
features the others lack.

Auth uniformly via `auth.Authorize` from
[`specs/4/9-acl-unified.md`](../4/9-acl-unified.md). `resreg.Caller` is
the surface-agnostic principal (`Sub`, `Name`, `Folder`, `Tier`,
`Claims`); `Claims` carries JWT predicates the ACL rows match against
(e.g. `operator=1`). Per-call resolvers populate it for each surface;
no scope-list shorthand survives — the ACL rows are the source.

## Inventory — today's writes

Every store write below is a candidate for `resreg` exposure. Columns:
**Today** = where it's invoked from; **MCP** = is there an existing
MCP tool; **REST** = is there an existing endpoint.

| Operation                           | Store call                                                     | Today                           | MCP                                                          | REST          |
| ----------------------------------- | -------------------------------------------------------------- | ------------------------------- | ------------------------------------------------------------ | ------------- |
| Group create                        | `PutGroup` (`store/groups.go:20`)                              | onbod/SetupGroup, CLI           | `register_group`                                             | —             |
| Group delete                        | `DeleteGroup` (`store/groups.go:47`)                           | CLI                             | —                                                            | —             |
| Route add / set / delete            | `AddRoute`/`SetRoutes`/`DeleteRoute` (`store/routes.go`)       | CLI, agent MCP, dashd           | `add_route`/`set_routes`/`delete_route` (`ipc/ipc.go:1252`+) | —             |
| User grant / ungrant                | `Grant`/`Ungrant` (`store/auth.go:175`)                        | CLI (`arizuko grant`)           | —                                                            | —             |
| Action grants (folder rule overlay) | `SetGrants` (`store/grants.go:17`)                             | agent MCP                       | `set_grants`                                                 | —             |
| Secret put / delete                 | `SetSecret`/`DeleteSecret` (`store/secrets.go:50`)             | dashd (`/dash/me/secrets`), CLI | —                                                            | dashd-private |
| Invite create / revoke              | `CreateInvite`/`RevokeInvite` (`store/invites.go`)             | CLI, onbod                      | —                                                            | onbod         |
| Identity create / link / unlink     | `CreateIdentity`/`LinkSub`/`UnlinkSub` (`store/identities.go`) | CLI                             | —                                                            | —             |
| Onboarding gates                    | `PutGate`/`DeleteGate`/`EnableGate` (`store/onboarding.go`)    | CLI                             | —                                                            | —             |
| Egress allowlist                    | `AddNetworkRule`/`RemoveNetworkRule` (`store/network.go`)      | crackbox register, CLI          | partial (register)                                           | —             |
| Web routes                          | `SetWebRoute`/`DelWebRoute` (`store/web_routes.go`)            | agent MCP                       | `set_web_route`/`del_web_route` (`ipc/ipc.go:1786`+)         | —             |
| Scheduled tasks                     | `schedule_task` family                                         | agent MCP                       | `schedule_task`+                                             | —             |
| Cost caps                           | `SetFolderCap`/`SetUserCap` (`store/cost_log.go:74`)           | CLI                             | —                                                            | —             |
| ACL rows (after spec 6/9)           | (`acl` table writes)                                           | n/a (new)                       | —                                                            | —             |

Columns with `—` are the gap. Most operator concepts are either
CLI-only with direct store calls (`cmd/arizuko/*.go`) or MCP-only with
no REST sibling. The shape is bimodal; spec 6/5's premise is to make
it uniform.

## Resource declarations to add

For each row above without a `resreg.Resource`, the declaration shape
is a small struct literal. Catalog of new resources:

| Resource          | Actions                                                                         | Owning daemon | Scope predicates                                                |
| ----------------- | ------------------------------------------------------------------------------- | ------------- | --------------------------------------------------------------- |
| `groups`          | list/get/create/update/delete                                                   | gated         | `admin` at scope ⊇ folder; `*` operator                         |
| `acl`             | list/get/create/delete                                                          | gated         | `admin` at scope ⊇ row.scope; `*` operator                      |
| `secrets`         | list/get/create/delete (no read of value via MCP — agent broker rule preserved) | gated         | folder-`admin` at scope, plus user-owned writes via dashd OAuth |
| `invites`         | list/get/create/revoke                                                          | onbod         | `admin` at scope ⊇ targetGlob                                   |
| `identities`      | list/get/create/link/unlink                                                     | gated         | self for own sub; `*` for cross-user link                       |
| `gates`           | list/get/put/delete/enable                                                      | onbod         | `*` operator                                                    |
| `network_rules`   | list/get/create/delete                                                          | gated         | folder-`admin` at scope                                         |
| `cost_caps`       | list/get/set                                                                    | gated         | `*` operator; self-read for own user                            |
| `scheduled_tasks` | (already partial — finish symmetry)                                             | timed         | folder-`admin` at scope                                         |
| `web_routes`      | (already MCP — add REST mirror)                                                 | webd          | folder-`admin` at scope                                         |

The pattern from `resreg/resreg.go` (`Resource` literal with
`Endpoints`, `MCPTools`, `Authz`, `Handler`, `Store`) covers each row.
New action = one struct literal addition + one handler function. The
handler is the only behavior; everything else is registration. Authz
delegates to `auth.Authorize`; for store-backed resources the adapter
threads a `*sql.Tx` in `Execution` so the mutation + audit row commit
as a unit.

## CLI evolution — `cmd/arizuko/*.go`

Today: `arizuko grant`, `arizuko invite`, `arizuko group add`, etc.
call `store.*` directly. The CLI binary opens `messages.db` and
writes rows. This is convenient on the host but bypasses every
authorization concern and every audit trail.

Target: each command becomes a thin client of the local MCP socket
(`/srv/data/arizuko_<inst>/ipc/root/socket`). The socket already
exists for `arizuko chat`. Two flavors of auth shape are possible:

- **Operator CLI as `folder:operator_cli` principal** — the socket is
  unix-domain, owned by the operator UID; presence on the socket
  proves operator capability. Implies an ACL row
  `(folder:operator_cli, '*', '**')` seeded at `arizuko create`.
  Simple, no OAuth needed for shell sessions.
- **Operator CLI as `google:114operator` after OAuth** — the CLI
  authenticates against a local OAuth flow (browser callback into the
  CLI), mints a session JWT, calls MCP carrying it. More work; matches
  the dashboard's auth shape exactly.

Lean: ship the unix-socket-as-capability path (option 1). Operators
already need filesystem access to run the CLI; an OAuth round-trip
adds nothing. The OAuth path remains available for remote CLI use
later (call `/v1/*` over HTTPS instead of MCP over the local socket).

## dashd evolution

Today: dashd is the operator web UI. Its read paths query the shared
DB directly; its few write paths (`/dash/me/secrets`) call `store.SetSecret`
directly.

Target: dashd's mutating handlers are thin shims over `resreg`
endpoints — they decode the form, build a `Caller` from the verified
session, then dispatch through the registry. Reads can stay direct
queries to the DB (cheap, read-only, no audit need) or migrate to
`GET /v1/<resource>` symmetrically. Lean: writes via registry, reads
direct for the dashboard's own UI. The `/v1/*` REST surface is for
external consumers; dashd is internal.

## Anti-patterns — what should NOT go via MCP

Some operations look like state changes but should not be exposed as
MCP tools. Each has the same shape: it's a hot path, a high-volume
internal event, or a stream rather than a CRUD verb.

- **Inbound message ingestion.** The gateway poll loop
  (`gateway/gateway.go:502+`) writes `messages` rows per inbound. This
  is per-message hot-path; pushing it through `resreg` would force a
  capability check and audit log per inbound. Not in scope. The agent
  can `inject_message` for synthetic sends — that IS an MCP tool and
  rightly so (audited, low-volume).
- **Cost-log writes** (`store/cost_log.go:20`). Every Claude API call
  emits a row. Per-call, not per-operator-action. Stays as a direct
  store write from `gateway` and `timed`.
- **Agent cursor advancement.** Internal bookkeeping, not user-facing.
- **Streaming surfaces.** Slink message stream, agent live output —
  spec 6/5 already calls these out of scope. SSE / WebSocket sits next
  to `resreg`, not inside it.
- **Auth session creation** (`store/auth.go:119`). The session is
  minted by `auth.Mint`, persisted by the auth library. Not a
  user-tool — it's the substrate every other tool consumes.
- **Migrations.** Schema changes are file-driven (`store/migrations/`)
  and run by `gated` at startup. Not a resource.

The rule: if it's user-initiated, audit-worthy, and fits an
allow/deny answer, it belongs in `resreg`. If it's a high-rate
side effect of normal operation, it does not.

## Phased rollout

| Milestone | Deliverable                                                                                                                                              |
| --------- | -------------------------------------------------------------------------------------------------------------------------------------------------------- |
| M0        | Audit complete (this spec's inventory + the table above). Catalog every `store.*` write site under `cmd/`, `dashd/`, `onbod/`, and direct callers.       |
| M1        | High-priority resources via `resreg`: `acl`, `groups`, `secrets`, `invites`. These cover the operator-facing core; the agent-facing tools already exist. |
| M2        | Backfill missing REST mirrors for existing agent MCP tools (`set_grants`, `register_group`, `add_route`, `set_web_route`). One PR per resource.          |
| M3        | Cutover `cmd/arizuko/*.go` to call the local MCP socket. Deletes direct `store.*` calls from `cmd/`. Single rollback point: re-enable the old code path. |
| M4        | Cutover dashd write handlers. `/dash/me/secrets` becomes a `secrets.create` dispatch. Drops the dashd-private REST path.                                 |
| M5        | Deprecate hand-written tools in `ipc/ipc.go` that have a registry equivalent; delete after one release.                                                  |

Each milestone is independent. Stopping at M2 still leaves the system
in a saner state (REST parity); M3+M4 are the structural wins.

## Auth shape for management operations

Under unified ACL (`specs/4/9-acl-unified.md`):

- **Operator human** — `(google:114operator, '*', '**')`. One row.
  All resources, all actions, everywhere.
- **Folder admin** — `(google:114alice, 'admin', 'atlas/**')`. Can
  manage routes/grants/secrets under `atlas/`, no further.
- **Operator agent** — `(folder:atlas, 'admin', 'atlas/**')`. The
  agent at the world root can administer its own subtree (delegate
  routes, set child grants). Same authority shape as the human
  folder admin, different principal namespace.
- **Leaf agent** — no `admin` rows; only `mcp:<tool>` rows derived
  from tier defaults. Same as today.

`auth.Authorize` is the only check. resreg's per-resource `Authz`
callback derives `(scope, params)` from the call and delegates —
there is no parallel predicate machinery. The
[`5/5`](5-uniform-mcp-rest.md) `<resource>:<verb>[:own_group]`
shorthand is the operator-token-minting affordance over the same
ACL rows.

`resreg` is the canonical mechanism. Existing `ipc/ipc.go` hand-rolled
tools migrate incrementally — see migration plan in
[`5/5` Phased rollout](5-uniform-mcp-rest.md). The shape was reshaped
post-oracle 2026-05-25 (per-invocation caller, tx-bound audit,
forwarder pattern); see [`5/5` Execution context](5-uniform-mcp-rest.md)
for the contract handlers must honour.

## Open questions

1. **CLI auth path.** Unix-socket-as-capability or OAuth-into-CLI?
   Spec leans socket; OAuth is the second option.
2. **dashd reads via registry or direct DB?** Symmetry argues
   registry; latency argues direct. Lean: writes via registry, reads
   direct for the dashboard's own UI.
3. **Secret value reads via MCP.** The broker invariant
   (`specs/11/11-crackbox-secrets.md`, referenced
   `specs/5/5-uniform-mcp-rest.md:144`) says the agent never reads
   secret values. The registry must enforce this — `secrets.get`
   returns metadata only, never the value. How to express in
   `resreg`? A per-action handler that scrubs the response, or a
   policy declaring "no value field in MCP responses ever"?
4. **Rate limits on management operations.** spec 6/5 punts to
   `specs/10/4-rate-limits.md`. ACL writes are low-volume; an
   accidental loop in the agent that calls `acl.create` every turn
   would fill the table fast. Need a write-rate cap somewhere.
5. **Backwards-compat during cutover.** M3 swaps CLI command bodies
   to MCP calls. If the local socket is down (gated crash) the CLI
   stops working — an emergency `--direct` flag for filesystem
   access, or a hard requirement that gated is up? Lean: require
   gated; surface a useful error.
6. **Cross-instance management.** Today each instance has its own
   socket. An operator running 5 instances calls `arizuko -i krons
grant ...` etc. The MCP cutover means 5 sockets, 5 connections.
   Trivial in shell, more interesting for tooling. Lean: don't
   solve; the per-instance shape matches the per-instance data dir
   shape.
7. **Audit emit contract.** Settled (2026-05-25): every state-changing
   op via `resreg` writes exactly one `audit_log` row, synchronous and
   transactional with the resource mutation — adapter calls
   `audit.EmitInTx(tx, ...)` _inside_ the same tx; on insert failure
   the mutation rolls back. Read-only ops emit slog telemetry only,
   no DB row. Forwarder resources (`Resource.Store == nil`, e.g.
   `webd/routes_mcp.go`) skip the local audit row — the downstream
   daemon writes it. slog → journald is separate operational telemetry
   (lossy, interactive); `audit_log` is the source of truth. Field
   schema: [`I-tool-call-logging.md`](I-tool-call-logging.md). Table
   definition: [`../6/F-audit-stream.md`](../6/F-audit-stream.md).
   Implemented in `resreg/resreg.go` via the `Execution.Tx` contract.
8. **Resource ownership across daemons.** `groups` is gated's;
   `invites` is onbod's; `web_routes` is webd's. Each registers its
   own resources. The MCP socket terminates in gated, so MCP calls
   to `invites.*` must forward to onbod over HTTP. Pattern shipped
   (2026-05-25): the forwarder is a `Resource{Store: nil}` whose
   `Handler` does an HTTP call downstream; the adapter skips the
   tx/audit dance, and the destination daemon writes the audit row.
   `webd/routes_mcp.go` is the canonical example.

## Code pointers

- `resreg/resreg.go` — `Resource` / `Execution` / `Handler` types,
  `RegisterREST`, `MCPTools` (per-invocation caller resolver), `invoke`
  (authz → tx → handler → audit → commit/rollback).
- `proxyd/resource.go` + `webd/routes_mcp.go` — the two existing call
  sites (routes resource, store-backed + forwarder respectively).
- `ipc/ipc.go:632`, `:646`, `:922` — existing wrappers (`granted`,
  `registerWithSecrets`, `regSocial`) that the registry replaces.
- `store/*.go` — every write call to migrate, per inventory above.
- `cmd/arizuko/*.go` — CLI entry points; today the direct-SQL site,
  M3 cutover target.
