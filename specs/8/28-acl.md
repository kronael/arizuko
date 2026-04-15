---
status: draft
phase: next
---

# ACL: glob-matched user_groups

No operator/user distinction in code. An operator is just a user with
`*` in their groups list. All access control is glob matching on the
`user_groups` table.

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

## Changes

### store/auth.go

`UserGroups` returns `[]string` (not pointer). No nil special case.
`*` and `**` are just strings in the list like any other entry.

### auth/jwt.go

`Claims.Groups` becomes `[]string` (not `*[]string`). Always present
in JWT. `[]` = no access, `["**"]` = all groups.

### auth/acl.go (new, ~15 lines)

`MatchGroups(allowed []string, folder string) bool` — shared helper.
Loops `allowed`, returns true if any entry matches `folder`:

- `**` → true
- otherwise `path.Match(entry, folder)` (covers `*`, literals, `pub/*`)

### proxyd/main.go

- `setUserHeaders`: always sets `X-User-Groups` header (no nil check)
- `davRoute`: use `MatchGroups` instead of prefix check + empty bypass

### webd/server.go

- `requireFolder`: empty header = deny. Use `MatchGroups` for check.

### groupfolder/folder.go

Block `*` and `**` as folder names (`reservedFolders`).

### Route creation (onbod, ipc)

Check `user_groups` via `MatchGroups` before inserting routes.

### Gateway message routing

No change. Routes authorized at creation time.

## Operator bootstrap

CLI (`arizuko group <instance> grant <sub> '**'`). First deploy seeds
via migration. No special operator concept — just a user with `**`.

## No existing users

Safe to change without data migration.
