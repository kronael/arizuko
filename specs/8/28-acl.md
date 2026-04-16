---
status: partial
phase: next
---

# ACL: glob-matched user_groups

No operator/user distinction in code. An operator is just a user whose
`user_groups` grant list contains `*` or `**`. All access control is
glob matching on the `user_groups` table.

## Implementation status (v0.28.0)

Done:

- `auth.MatchGroups(allowed, folder)` — shared helper (`auth/acl.go`).
- `groupfolder` reserves `*` and `**` as folder names.
- `onbod/handleCreateWorld` authorizes route creation against
  `user_groups` via `MatchGroups` (operator short-circuit).
- `proxyd.davRoute` uses `MatchGroups` in place of the prefix check.
- `store.UserGroups(sub) *[]string` — nil for operator (`*` or `**` row),
  slice otherwise.

Pending:

- CLI `arizuko group <instance> grant/ungrant/grants` subcommand surface
  (store methods exist in WIP; CLI wiring incomplete).
- `webd/server.go` — no `requireFolder` exists yet.
- Route creation in `ipc` package (the MCP `add_route`/`set_routes` tools)
  currently has no per-user ACL; still operator-only by convention.

## Semantics

- `user_groups` entries are glob patterns matched against folder paths
- `*` matches any single path segment (not `/`)
- `**` matches everything including `/` (all groups, subtrees)
- No rows = no access
- Matching uses Go `path.Match` for `*`, special-case `**` for cross-slash

Examples:

| user_groups entry | matches                      |
| ----------------- | ---------------------------- |
| `*`               | `alice`, `bob` (root groups) |
| `**`              | everything                   |
| `pub/*`           | `pub/alice`, `pub/bob`       |
| `pub/alice`       | `pub/alice` only             |
| `krons`           | `krons` only                 |

## Types

### store/auth.go

`UserGroups(sub) *[]string`:

- Nil pointer → operator (unrestricted). Caller skips any ACL check.
- Non-nil slice (possibly empty) → enforce `MatchGroups`.

A user is "operator" if they have a `*` or `**` row in `user_groups`.
The lookup returns nil in that case.

### auth/jwt.go

`Claims.Groups *[]string` — mirrors the store pointer semantics.

- Nil in JWT (`omitempty`) → operator.
- Non-nil slice → enforce `MatchGroups`.

The spec previously said `Groups []string` with `["**"]` = operator.
That was aspirational; the implementation chose the pointer form so a
missing `groups` claim can't be forged as `[]` (= no access) — a
malformed or trimmed token must fail closed, not auto-promote to
no-access. The nil/operator convention is centralised in `store` and
`auth` and propagated through `proxyd.setUserHeaders`:

- Operator → no `X-User-Groups` header set.
- Non-operator → `X-User-Groups` is JSON-encoded slice (possibly `[]`).

Downstream (`davRoute`, onbod, others) treat missing header as operator.

### auth/acl.go (new)

`MatchGroups(allowed []string, folder string) bool` — shared helper.
Loops `allowed`, returns true if any entry matches `folder`:

- `**` → true
- otherwise `path.Match(entry, folder)` (covers `*`, literals, `pub/*`)

Nil/empty `allowed` → false. The caller is responsible for recognising
the operator case (nil pointer) separately; `MatchGroups` only models
"is this folder covered by at least one grant".

### proxyd/main.go

- `setUserHeaders`: sets `X-User-Groups` only when the user is
  non-operator (groups pointer is non-nil). Missing header therefore
  means operator.
- `davRoute`: missing header → pass through. Present but empty → deny.
  Otherwise use `MatchGroups`.

### webd/server.go

Not yet present. When added, `requireFolder` will mirror `davRoute`:
empty header = operator = allow. Non-empty = `MatchGroups`.

### groupfolder/folder.go

`*` and `**` are reserved folder names (`reservedFolders`) — users
cannot create workspaces that collide with ACL patterns.

### Route creation (onbod, ipc)

Onbod's create-world flow checks `user_groups` via `MatchGroups` before
inserting routes. The create-world path always self-grants first, so the
check currently gates only future second-JID flows and protects against
accidental cross-user route inserts in the same code path.

IPC route MCP tools still require tier-0 (operator) invocation — ACL
check there is a follow-up.

### Gateway message routing

No change. Routes authorized at creation time.

## Operator bootstrap

CLI (`arizuko group <instance> grant <sub> '**'`). First deploy seeds
via migration. No special operator concept in code — just a user with
`**` (or `*` for root-only).

## No existing users

Safe to change without data migration.
