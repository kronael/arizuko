---
status: shipped
shipped: 2026-04-29
depends: 9-crackbox-standalone
---

# Crackbox in arizuko — long-running daemon consumer

> arizuko runs `crackbox proxy serve` as a compose service and
> POSTs Register/Unregister per agent spawn. Same wire shape as
> today's egred prototype — only the binary name and Go import
> path change.

## Status

Planned. The `egred/` daemon shipped 2026-04-29 is the working
prototype this spec replaces. No semantic change is intended by
this rework.

## Today's prototype

`egred/` is the forward-proxy daemon currently running in
production on krons. It demonstrated default-deny works
end-to-end: an agent spawn registered with egred,
`https://api.anthropic.com` was allowed, and Claude Code's own
datadog telemetry (`http-intake.logs.us5.datadoghq.com`) was
blocked by the seed allowlist failing closed. That's the property
this rework preserves.

What's wrong with the prototype: it lives in `arizuko/egred/`
under the arizuko module, not as a sibling component. Its
allowlist resolver (`store/network.go`) reads arizuko's
`messages.db`. Its matcher keys IPs to "folders" because that's
what gated hands it. The runtime artifact is generic but the
source is tangled with arizuko's domain. See
[`specs/8/b-orthogonal-components.md`](../8/b-orthogonal-components.md)
for the layering principle.

This spec describes the rename + extraction of that prototype
into the `crackbox` sibling component without semantic change.

## Architecture

arizuko runs `crackbox proxy serve` as a Docker compose service.
One shared daemon per arizuko instance. The crackbox container
sits on the project's `agents` internal Docker network plus the
default bridge — agents can only reach the world by going
through it.

For each agent spawn, arizuko:

1. Computes the flat allowlist via
   `store.ResolveAllowlist(folder)` — the existing folder-walk +
   dedupe.
2. POSTs `/v1/register {ip, id, allowlist}` to the crackbox admin
   API using `crackbox.Client`.
3. Spawns the agent container on the `agents` network with
   `HTTPS_PROXY=http://crackbox:3128`.
4. On exit, POSTs `/v1/unregister {ip}`.

Wire shape is identical to today's egred. The proxy daemon is
unchanged in behavior — only its package path and binary name move.

## Domain vs mechanism boundary

| Owner    | What                                                                       |
| -------- | -------------------------------------------------------------------------- |
| arizuko  | `network_rules` table + migration 0037                                     |
| arizuko  | `store.ResolveAllowlist` folder-walk and dedupe                            |
| arizuko  | `arizuko network <instance>` operator CLI                                  |
| arizuko  | `container/egress.go` lifecycle glue                                       |
| arizuko  | `EGRESS_ISOLATION` toggle                                                  |
| arizuko  | Default seed allowlist (`anthropic.com`, `api.anthropic.com`) in migration |
| crackbox | The proxy daemon                                                           |
| crackbox | `matchHost` and the domain validators                                      |
| crackbox | The `/v1/register` etc admin API                                           |
| crackbox | The `:3128` proxy listener                                                 |

arizuko hands crackbox a flat `(ip, id, []string)`. crackbox
never learns about folders, grants, ancestry, or `messages.db`.

## Migration delta from today's egred

One refactor commit
(`[refactor] move egred → crackbox component (specs 6/9 + 6/10)`):

- Rename `arizuko/egred/` → `arizuko/crackbox/`.
- Move daemon code into the standard layout per spec 6/9:
  `crackbox/cmd/crackbox/main.go`, `crackbox/pkg/proxy/`,
  `crackbox/pkg/match/`, `crackbox/pkg/admin/`,
  `crackbox/pkg/client/`.
- Add the `crackbox run` subcommand and `crackbox/pkg/run/`
  (new code required by spec 6/9 — not used by arizuko but ships
  in the same binary).
- `arizuko/container/egress.go` switches from inline HTTP POST to
  `crackbox.Client.Register` / `Unregister`.
- `arizuko/compose/compose.go` emits a `crackbox` service
  (`image: arizuko-crackbox`,
  `entrypoint: ["crackbox", "proxy", "serve"]`) instead of
  `egred`. Same wire shape, new name.
- Drop arizuko-internal imports from the crackbox tree. Verify:
  `grep -rE 'github.com/[^/]+/arizuko/(store|core|gateway|api)' arizuko/crackbox/`
  returns empty.
- Keep `arizuko/cmd/arizuko/network.go`, `arizuko/store/network.go`
  and migration 0037 in arizuko — folder ancestry is arizuko
  domain.
- Optionally rename `EGRED_API` env to `CRACKBOX_API` for
  consistency.

## No semantic change

krons behavior before and after the migration is identical.
Default-deny still works. `EGRESS_ISOLATION=true` still enforces
the proxy. Existing smoke tests still pass. The wire format on
`/v1/register` and the matcher behavior in `matchHost` are
preserved exactly.

## Out of scope

- Spec 6/11 placeholder injection.
- MCP tools (`request_network`, `list_network_rules`) — CLI only
  for now.
- Per-user network rules (per-folder only).
- Traffic logging and audit.
- Response scanning.

## Acceptance

Same as today's verified end-to-end, after the rework:

- Agent spawn on krons triggers `crackbox.Client.Register` and
  the journal shows
  `egress registered folder=<f> ip=<ip> rules=<n>`.
- A request to `https://api.anthropic.com` from the agent is
  allowed; a request to `https://datadoghq.com` is denied by the
  proxy.
- `arizuko network krons resolve atlas` returns the seeded list
  (`anthropic.com`, `api.anthropic.com`) plus any folder additions.
- All existing tests pass.
- `grep -rE 'github\.com/[^/]+/arizuko/(store\|core\|gateway\|api\|chanlib\|chanreg\|router\|queue|ipc\|grants\|onbod|webd|gated)' arizuko/crackbox/` returns
  empty.
