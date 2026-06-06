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
  - `arizuko generate <instance>` — write `docker-compose.yml`
  - `arizuko run <instance>` — generate + `docker compose up`
  - `arizuko status <instance>` — show compose services + channels
  - `arizuko pair <instance> <svc>` — `docker compose run --rm`
  - `arizuko group <inst> list | add | rm | grant | ungrant | grants`
  - `arizuko gate  <inst> list | add | rm | enable | disable`
  - `arizuko identity <inst> list | link <sub> [--name N] [--id ID] | unlink <sub>` — manage identity ↔ sub links
  - `arizuko network <inst> allow|deny <folder> <target> | list | resolve <folder>` — per-folder egress allow/deny rules
  - `arizuko secret <inst> set <folder> KEY --value V | list <folder> | delete <folder> KEY` — folder-scoped secrets
  - `arizuko user-secret <inst> set <user_sub> KEY --value V | list <user_sub> | delete <user_sub> KEY` — user-scoped secrets
  - `arizuko token <inst> issue chat <folder> [<suffix>] | issue webhook <folder> <label> | list <folder> | revoke <jid> [<owner_folder>]` — manage `route_tokens` (chat / webhook capability URLs)
  - `arizuko invite <inst> create <target_glob> [--max-uses N] [--expires DURATION]`
  - `arizuko invite <inst> list [--issued-by SUB]`
  - `arizuko invite <inst> revoke <token>`
  - `arizuko send <inst> <folder> [<message>] [--wait | --stream] [--stdin] [--from <sender>] [--topic <topic>] [--token <raw>]` — inject a message into a folder's queue (uses topic for conversation continuity). Default is **operator-direct**: no token, writes the inbound straight to the DB on `web:<folder>` (the operator already owns the DB, same authority as `create`/`grant`/`secret`); the gateway poll loop runs the agent and `--wait`/`--stream` prints its reply. Pass `--token`/`ARIZUKO_CHAT_TOKEN` to instead POST the public `/chat/<token>` endpoint as a non-operator caller.
  - `arizuko budget <inst> set folder|user <name> --daily N` / `show folder|user <name>` — per-folder or per-user daily spend cap in cents (0 = uncapped); pre-spawn gate enforces lower of (folder cap, user cap)
  - `arizuko apply <inst> <manifest.yaml> [--force]` — restore cold-tier config from a YAML dump in one tx; CAS-checks `config_version` (spec 5/36)
  - `arizuko plan <inst> <manifest.yaml>` — non-mutating diff of a manifest vs live config
  - `arizuko get <inst> <resource>` — emit one resource's live rows as a YAML fragment (round-trips to a no-op)
  - `arizuko export <inst> [out.yaml]` — dump cold-tier config to canonical-ordered YAML
  - `arizuko chat <instance>` — interactive Claude Code session bound to root MCP socket

## Dependencies

- `compose`, `container`, `core`, `store`

## Files

- `main.go` — command dispatch + `create`/`generate`/`run`/`status`/`pair`/`group`/`gate`/`invite`/`identity`/`chat`
- `apply.go` — `apply`/`plan`/`get`/`export` (YAML manifests, spec 5/36)
- `budget.go` — `budget` spend caps
- `network.go` — `network` egress rules
- `secret.go` — `secret` + `user-secret`
- `send.go` — `send` message injection
- `token.go` — `token` route-token management

## Related docs

- Top-level `README.md` (Quick Start)
- `ARCHITECTURE.md`
