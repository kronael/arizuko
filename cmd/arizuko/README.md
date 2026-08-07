# cmd/arizuko

CLI entrypoint. Builds the `arizuko` binary.

## Purpose

Operator-facing commands: instance creation, compose generation, group
and gate administration, status. Runs locally (no daemon dependency
beyond `docker` for `run` and `pair`).

## Entry points

- Binary: `cmd/arizuko/main.go` → `./arizuko`
- Commands:
  - `arizuko create <name> [--product <product>]` — seed data dir from `template/env.example`; `--product` copies skills and facts from `ant/examples/<product>/` and prints the env checklist
  - `arizuko generate <instance>` — write `docker-compose.yml` + the compose-managed `.env` block. Also relinks any installed fragment that is still a byte-identical copy of the bundled catalog into a symlink there (`compose.RelinkCatalog`) — no flag, runs on every generate/deploy. A fragment that has diverged (a hand edit, even comment-only) or whose filename encodes a multi-account variant (`<kind>-<label>.yml`) is left as a real file, never touched. Requires `HOST_APP_DIR` in `.env` to be a host-resolvable path — see `compose/README.md` "Packages"
  - `arizuko packages <inst> list | add <name> | install <source-dir> | upgrade <name> | sync | remove <name>` — `add` links a bundled-catalog adapter fragment at the catalog (`services/<name>.yml` + optional `<name>-routes.json`, spec 5/27) rather than copying it; falls back to a copy with a warning when `HOST_APP_DIR` isn't set. `install <source-dir>` installs a package from a git URL or local dir (compose fragment, `*-routes.json` proxyd route, `*-grants.json` acl, `skills/<name>/`), recording an `installed_packages` row so `upgrade` (refuses a dirty asset) and `remove` (deletes exactly what was recorded) are exact (spec 5/28)
  - `arizuko packages <inst> sync` — `upgrade` over every installed package in one pass: re-applies each record's fragment from its recorded source, reports `updated` / `unchanged` / `SKIPPED`, and writes nothing for a package already at its recorded revision. A locally edited asset is reported and skipped rather than fatal, so one hand-edited fragment can't block the rest of the instance. Not to be confused with `generate`'s catalog relink above — that converts an identical copy into a symlink and never reads the installed-package record (spec 5/28)
  - `arizuko products <inst> apply <folder>` — blend the ordered product mix declared in `groups/<folder>/products.toml` (`[[product]]` blocks with a `source =`; relative sources join `HOST_APP_DIR`) into that group, per payload kind, and record one `installed_packages` row per product under `(folder, name)` — the only writer of a non-empty folder. Idempotent; a managed asset edited since the last apply is reported and skipped, never overwritten (spec 5/28). `packages sync` re-applies every group's mix too
  - `arizuko run <instance>` — generate + `docker compose up`
  - `arizuko status <instance>` — show compose services + channels
  - `arizuko pair <instance> <svc>` — `docker compose run --rm`
  - `arizuko group <inst> list | add | rm | grant | ungrant | grants`
  - `arizuko gate  <inst> list | add | rm | enable | disable`
  - `arizuko network <inst> allow|deny <folder> <target> | list | resolve <folder>` — per-folder egress allow/deny rules
  - `arizuko secret <inst> set <folder> KEY --value V | list <folder> | delete <folder> KEY` — folder-scoped secrets
  - `arizuko user-secret <inst> set <user_sub> KEY --value V | list <user_sub> | delete <user_sub> KEY` — user-scoped secrets
  - `arizuko token <inst> issue chat <folder> [<suffix>] | issue webhook <folder> <label> | list <folder> | revoke <jid> [<owner_folder>]` — manage `route_tokens` (chat / webhook capability URLs)
  - `arizuko token <inst> issue bearer <folder> --scope|-s s1,s2 [--ttl|-t 1h] [--sub SUB]` — mint a folder-scoped ES256 access JWT signed with authd's active key (read from auth.db; operator-only). E.g. `--scope messages:write,messages:read` for anteval's inject+inspect
  - `arizuko invite <inst> create <target_glob> [--max-uses N] [--expires DURATION]`
  - `arizuko invite <inst> list [--issued-by SUB]`
  - `arizuko invite <inst> revoke <ref>`
  - `arizuko send <inst> <folder> [<message>] [--wait | --stream] [--stdin] [--from <sender>] [--topic <topic>] [--token <raw>]` — inject a message into a folder's queue (uses topic for conversation continuity). Default is **operator-direct**: no token, writes the inbound straight to the DB on `web:<folder>` (the operator already owns the DB, same authority as `create`/`grant`/`secret`); the gateway poll loop runs the agent and `--wait`/`--stream` prints its reply. Pass `--token`/`ARIZUKO_CHAT_TOKEN` to instead POST the public `/chat/<token>` endpoint as a non-operator caller.
  - `arizuko budget <inst> set folder|user <name> --daily N` / `show folder|user <name>` — per-folder or per-user daily spend cap in cents (0 = uncapped); pre-spawn gate enforces lower of (folder cap, user cap)
  - `arizuko apply <inst> <manifest.yaml> [--force] [--as-folder <folder>]` — restore cold-tier config from a YAML dump, one tx per owner DB, with a pre-image rollback if a later one fails. CAS is a content hash recomputed from the live rows, not a stored counter; `--force` skips it. Refuses (even with `--force`) if a row names a folder that is not a group. `--as-folder` re-scopes a single-folder manifest onto another folder (spec 5/8)
  - `arizuko plan <inst> <manifest.yaml>` — non-mutating diff of a manifest vs live config; also reports a missing-group refusal `apply` would raise
  - `arizuko get <inst> <resource>` — emit one resource's live rows as a YAML fragment (round-trips to a no-op)
  - `arizuko export <inst> [out.yaml]` — dump cold-tier config to canonical-ordered YAML
  - `arizuko archive export <inst> [out.tar] [--quiesced]` — full-instance backup: the config dump plus secret values, message history, pending onboarding admissions and every `groups/<folder>/` tree, as one tar (spec 5/8)
  - `arizuko archive apply <inst> <archive.tar> [--force] [--stopped]` — full restore. Claims each folder's runed run slot before touching its tree, so an unreachable runed is fatal; `--stopped` asserts the instance is down instead. Credential-bearing documents (`route_tokens`, `invites`, `onboarding`) restore only with `--force` onto a proven-empty target
  - `arizuko chat <instance>` — interactive Claude Code session bound to root MCP socket

## Dependencies

- `compose`, `container`, `core`, `routd` (installed-package record), `store`

## Files

- `main.go` — command dispatch + `create`/`generate`/`run`/`status`/`pair`/`group`/`gate`/`invite`/`chat`
- `apply.go` — `apply`/`plan`/`get`/`export` (YAML manifests, spec 5/8), the missing-group preflight, and the cross-subsystem pre-image rollback both `apply` and `archive apply` share
- `archive.go` — `archive export`/`archive apply` (full-instance backup, spec 5/8)
- `retarget.go` — `apply --as-folder`, the recipe over `resreg.Resource.Retarget`
- `budget.go` — `budget` spend caps
- `network.go` — `network` egress rules
- `packages.go` — `packages` catalog `add` (spec 5/27) + source-based `install`/`upgrade`/`sync`/`remove` with the `installed_packages` record (spec 5/28); `reapplyPackage` is the single updater `upgrade` and `sync` share
- `products.go` — `products apply`: the `products.toml` reader + the group-scoped half of the 5/28 lock; the blend itself is `container/blend.go`
- `secret.go` — `secret` + `user-secret`
- `send.go` — `send` message injection
- `token.go` — `token` route-token management

## Related docs

- Top-level `README.md` (Quick Start)
- `ARCHITECTURE.md`
