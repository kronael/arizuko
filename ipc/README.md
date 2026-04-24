# ipc

MCP server on unix socket — the in-container agent's only way to the host.

## Purpose

One MCP server per group at `ipc/<folder>/gated.sock`. Action tools
(`send_*`, `schedule_task`, `register_group`, `set_routes`, …) mutate
state; the `inspect_*` family is read-only introspection. Tools are
filtered per-caller by `auth.Authorize` against derived grant rules.
Identity (folder, tier) is resolved from the socket path; peer UID is
checked to reject cross-user access.

## Public API

- `ServeMCP(sockPath string, gated GatedFns, db StoreFns, folder string, rules []string, expectedUID int) (stop func(), err error)`
- `GatedFns` — callbacks into gated (enqueue, register channel, run container)
- `StoreFns` — callbacks into store (typed subset, not the full `*store.Store`)
- `PlatformHistory`, `ErroredChat`, `TaskRunLog` — DTO types

## Dependencies

- `core`, `auth`, `grants`, `router`, `mark3labs/mcp-go`

## Files

- `ipc.go` — server wiring, action tools, peer-cred check
- `inspect.go` — read-only introspection (`inspect_messages`, `inspect_routing`, `inspect_tasks`, `inspect_session`)
- `SECURITY.md` — threat model (peer UID, path resolution, grants)

## Related docs

- `ARCHITECTURE.md` (IPC section)
- `specs/7/33-inspect-tools.md`
- `SECURITY.md` — root threat model
