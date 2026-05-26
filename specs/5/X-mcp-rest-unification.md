---
status: drafting
depends: specs/5/5-uniform-mcp-rest.md
---

# specs/5/X — MCP + REST unification (finish what 6/5 started)

## Why

The platform thesis (`specs/7/index.md`) says agents and humans see
the same actions through the same auth gate. `specs/5/5-uniform-mcp-rest.md`
established the principle: "every resource is reachable via both MCP
(for agents) and REST (for humans / external tools) through one
hand-written handler — no auto-generated DSL, no catalog-driven
mapper." It is partly shipped. This spec closes the gap.

Without full coverage:

- The git reconcile loop (Action 3) has two surfaces to watch for
  mutations. Two surfaces drift silently.
- Operators learn one API; agents learn another. Skills authored for
  one don't work on the other.
- `arizuko apply` (Tier A) cannot trust that all state transitions
  hit a single audit path.

## Scope — what counts as "unified"

For each resource, one Go handler exposes:

- **Read**: list + get
- **Write**: create + update + delete (where the resource model
  permits)
- **Scoped query** filters honored uniformly

Both surfaces share:

- **Auth gate** — `auth.RequireSigned` for REST; `SO_PEERCRED` +
  grant check for MCP. Same grant DSL (`acl.Authorize`) on both
  paths.
- **Validation** — one validator function, called by both wrappers.
- **Errors** — one error type with REST-status + MCP-error mapping
  done at the wrapper, not inside the handler.
- **Audit** — the handler emits one structured event; the wrapper
  is transport-only.

## Inventory — what's covered today, what isn't

Read these to confirm before implementing (paths are post-renumber):

- `gateway/` — current resource handlers
- `ipc/` — MCP tool implementations
- `webd/`, `dashd/`, `onbod/`, `proxyd/` — REST surfaces
- `specs/5/5-uniform-mcp-rest.md` — the canonical spec

Resources to verify uniform coverage on (non-exhaustive, refine
during implementation):

| Resource          | MCP tool                                                 | REST endpoint                  | Unified?                    |
| ----------------- | -------------------------------------------------------- | ------------------------------ | --------------------------- |
| chats             | `list_chats`, `get_chat`, ...                            | `GET /v1/chats`, ...           | check                       |
| routes            | `list_routes`, `set_routes`, `add_route`, `remove_route` | `GET/POST/DELETE /v1/routes`   | check                       |
| grants            | `list_grants`, `add_grant`, ...                          | `/v1/grants`                   | check                       |
| secrets           | `list_secrets`, `add_secret`, ...                        | `/v1/secrets` (likely partial) | check                       |
| groups            | `list_groups`, `get_group`, ...                          | `/v1/groups`                   | check                       |
| products (NEW)    | `list_products`, ...                                     | `/v1/products`                 | new — defined in this phase |
| deployments (NEW) | `list_deployments`, ...                                  | `/v1/deployments`              | new                         |

Build the actual matrix during implementation; commit it as
`specs/5/X-coverage.md` (or expand this spec inline) once
inventoried.

## Non-goals

- Auto-generating handlers from a schema. The principle is hand-rolled.
- Replacing MCP with REST or vice-versa. Both are first-class.
- Authorization rewrite. Reuse `acl.Authorize` and the unified ACL
  spec (`specs/4/9-acl-unified.md`).
- Versioning the REST API. v1 stays v1; additive changes only.

## Implementation pattern (one resource at a time)

```go
// handler — pure logic, no transport
type ChatsService struct { store Store; clock Clock }

func (s *ChatsService) List(ctx context.Context, actor Actor, q ChatsQuery) ([]Chat, error) {
    if err := acl.Authorize(actor, "chats:list", q.Scope); err != nil { return nil, err }
    if err := validateChatsQuery(q); err != nil { return nil, err }
    chats, err := s.store.ListChats(ctx, q)
    if err != nil { return nil, err }
    audit.Emit(ctx, "chats.list", actor, q, len(chats))
    return chats, nil
}

// MCP wrapper — auth from SO_PEERCRED, JSON-RPC marshalling
func (h *MCPHandler) handleListChats(ctx context.Context, req mcp.Req) mcp.Resp { ... }

// REST wrapper — auth from signed headers, HTTP marshalling
func (h *RESTHandler) handleGetChats(w http.ResponseWriter, r *http.Request) { ... }
```

Both wrappers are thin. The handler is the only place business
logic lives.

## Testing

Per-resource integration test that exercises both wrappers against
the same handler instance. Asserts:

1. Same input → same output across MCP and REST.
2. Same auth failure → same error code (translated per protocol).
3. Same validation failure → same error code.
4. Same audit event emitted (one row in `audit_log`, NOT two).

## Acceptance

- Coverage matrix has zero "no" rows.
- Per-resource integration tests pass (`make test -short`).
- `dashd` operator UI works against REST only.
- A skill running in a container works via MCP only.
- The audit log shows exactly one row per state transition,
  regardless of which surface initiated it.

### Audit contract

Every state-changing handler MUST write exactly one `audit_log` row in
the SAME database transaction as the resource mutation; if the audit
row write fails, the mutation MUST roll back. Read-only handlers emit
slog telemetry only — no audit row. Field schema:
[`../5/I-tool-call-logging.md`](../5/I-tool-call-logging.md). Table
definition: [`../6/F-audit-stream.md`](../6/F-audit-stream.md).

## Open questions

- Streaming endpoints (server-sent events on REST, MCP notifications
  on the agent side) — should they share a handler too? Today they
  diverge. Defer or unify?
- Pagination shape — MCP tools return arrays today; REST uses
  cursor pagination on some routes. Harmonize or leave per-resource?
