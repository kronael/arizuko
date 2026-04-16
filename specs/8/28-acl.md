---
status: implemented
phase: next
---

# ACL: glob-matched user_groups

No operator/user distinction in code. An operator is just a user whose
`user_groups` grant list contains `**`. All access control is glob
matching on the `user_groups` table.

## Semantics

- `user_groups` entries are glob patterns matched against folder paths
- `**` matches everything (operator is implicit — `**` in the list is
  the only operator signal)
- other patterns use Go `path.Match` (`*` matches one path segment)
- No rows = no access

Examples:

| user_groups entry | matches                |
| ----------------- | ---------------------- |
| `**`              | everything (operator)  |
| `*`               | any single segment     |
| `pub/*`           | `pub/alice`, `pub/bob` |
| `pub/alice`       | `pub/alice` only       |
| `krons`           | `krons` only           |

## Types

### auth/acl.go

`MatchGroups(allowed []string, folder string) bool` — shared helper.
Returns true if any entry matches `folder`:

- `**` → true (operator)
- otherwise `path.Match(entry, folder)`

Nil/empty `allowed` → false. No operator short-circuit outside this
function — callers just call `MatchGroups`.

### store/auth.go

`UserGroups(sub) []string` — returns grant rows verbatim, `**`
included. Operator is implicit: the `**` entry is just another
pattern that happens to match everything. No nil-sentinel.

### auth/jwt.go

`Claims.Groups []string` — plain slice serialized with `omitempty`.
`["**"]` marks an operator claim; any other content is a regular grant
list.

### proxyd/main.go

- `setUserHeaders`: always emits `X-User-Groups` (JSON-encoded slice).
- `davRoute`: parses the header and calls `MatchGroups` for the leaf
  group. Bare `/dav` redirects to the user's first non-`**` grant, or
  `/dav/root/` if none.

### webd/server.go

`requireFolder`: parses `X-User-Groups`, then admits iff the folder
matches `**`, exactly, or is nested under a grant (grant `atlas` also
covers `atlas/child`). No operator short-circuit.

### groupfolder/folder.go

`*` and `**` are reserved folder names (`reservedFolders`) — users
cannot create workspaces that collide with ACL patterns.

### Route creation (onbod)

Create-world flow authorizes via `userGroups` + `MatchGroups`. The path
always self-grants first, so the check gates future second-JID flows
and protects against accidental cross-user route inserts.

IPC route MCP tools still gate at tier-0 — per-user ACL there is a
follow-up.

## Operator bootstrap

CLI: `arizuko group <instance> grant <sub> '**'`. First deploy seeds
via migration. No special operator concept in code — just a user with
`**`.
