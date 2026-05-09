# dashd

Operator dashboard daemon: HTMX views over `messages.db` plus
allow-listed memory editing on disk.

## Purpose

Standalone HTMX portal for operators. Aggregator UI: owns no tables,
renders pages by reading data from sibling daemons. Auth is enforced
upstream by `proxyd`'s `requireAuth` middleware; dashd itself assumes
the caller is authorized and does not further scope responses by group.

## Tables owned

None. Per `specs/6/R-platform-api.md`, dashd is purely a `/v1/*` client
of gated, timed, webd, onbod.

## Surface

Shipped today (see `main.go`): 12 GET routes returning HTML/HTMX, plus
two memory-edit verbs.

- `GET /dash/` — portal
- `GET /dash/status/`, `GET /dash/tasks/`, `GET /dash/activity/`,
  `GET /dash/groups/`, `GET /dash/memory/`, `GET /dash/profile/`
- `GET /dash/tasks/x/list`, `GET /dash/activity/x/recent` — HTMX partials
- `PUT|DELETE /dash/memory/{folder}/{rel}` — allow-listed file edits
  (`MEMORY.md`, `.claude/CLAUDE.md`, flat `*.md` under
  `diary/`, `facts/`, `users/`, `episodes/`)
- `GET /health`

Today these read multiple tables directly from the shared DB
(`groups`, `routes`, `scheduled_tasks`, `messages`, `sessions`,
`channels`, `auth_users`) and edit memory via direct fs access.

## Future per spec

dashd holds an operator session token (issued by `proxyd` at OAuth
login) and makes `/v1/*` calls to sibling daemons to render its pages.
Adds write paths (forms POSTing to `POST/PATCH/DELETE` of the relevant
daemon) wherever today's UI is read-only. After this refactor, dashd
never touches tables directly.

Migration table (from `specs/6/R-platform-api.md ## Dashboard`):

| dashd page        | Today (direct DB)                      | Future (`/v1/*` client)                                     |
| ----------------- | -------------------------------------- | ----------------------------------------------------------- |
| `/dash/groups/`   | reads `groups`, `routes`               | `gated/v1/groups`, `gated/v1/routes`                        |
| `/dash/tasks/`    | reads `scheduled_tasks`                | `timed/v1/tasks` (+ form → `POST timed/v1/tasks`)           |
| `/dash/activity/` | reads `messages` LIMIT 50              | `gated/v1/messages?limit=50&order=desc`                     |
| `/dash/status/`   | reads `groups`, `sessions`, `channels` | `gated/v1/groups`, `gated/v1/sessions`, `gated/v1/channels` |
| `/dash/memory/`   | direct fs read/write                   | `gated/v1/files/*` (or whichever daemon owns the group fs)  |
| `/dash/profile/`  | reads `auth_users`                     | `onbod/v1/users/{sub}`                                      |

## Token contract

- dashd VERIFIES the operator's session token on its own routes via
  `auth.VerifyHTTP` (header signed by `proxyd`).
- dashd FORWARDS that token as `Authorization: Bearer ...` when calling
  sibling daemons' `/v1/*`.
- dashd never mints. Issuance lives at proxyd (user sessions), MCP host
  (agent caps), onbod (invite redemption).

## Entry points

- Binary: `dashd/main.go`
- Listen: `$DASH_PORT` (default `:8080`, also `PORT`)

## Dependencies

- `chanlib` (env helpers), `diary`, `theme`
- Future: `auth` (token verify + forward), HTTP clients for
  `gated`, `timed`, `webd`, `onbod`.

## Configuration

- `DATA_DIR` or `DB_PATH` — resolves `<DATA_DIR>/store/messages.db`
  (used today; goes away once reads migrate to `/v1/*`)
- `DASH_PORT` — listen port
- `INSTANCE_NAME` — shown in portal header

## Health signal

`GET /health` returns 200 when DB is reachable. Typical deploy reaches
dashd through `proxyd` at `/dash/`; direct exposure requires `DASH_PORT`
mapped on the host.

## Files

- `main.go` — HTMX handlers, safe path join, capped file reads

## Related docs

- `specs/6/R-platform-api.md` — federated `/v1/*` contract + dashd
  migration table
- `specs/4/Q-dash-memory.md` — memory view & edit
- `specs/7/25-dashboards.md`
- `ARCHITECTURE.md` (Operator Dashboard section)
