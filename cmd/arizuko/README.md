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
  - `arizuko invite <inst> create <target_glob> [--max-uses N] [--expires DURATION]`
  - `arizuko invite <inst> list [--issued-by SUB]`
  - `arizuko invite <inst> revoke <token>`
  - `arizuko send <inst> <folder> [<message>] [--wait | --stream] [--stdin] [--topic <topic>]` — inject a message into a folder's queue (uses topic for conversation continuity)
  - `arizuko budget <inst> set folder|user <name> --daily N` / `show folder|user <name>` — per-folder or per-user daily spend cap in cents (0 = uncapped); pre-spawn gate enforces lower of (folder cap, user cap)
  - `arizuko apply <inst> <manifest.yaml> [--force]` — restore cold-tier config from a YAML dump in one tx; CAS-checks `config_version` (spec 5/36)
  - `arizuko plan <inst> <manifest.yaml>` — non-mutating diff of a manifest vs live config
  - `arizuko get <inst> <resource>` — emit one resource's live rows as a YAML fragment (round-trips to a no-op)
  - `arizuko export <inst> [out.yaml]` — dump cold-tier config to canonical-ordered YAML
  - `arizuko chat <instance>` — interactive Claude Code session bound to root MCP socket

## Dependencies

- `compose`, `container`, `core`, `store`

## Files

- `main.go` — command dispatch, each `cmd*` function

## Related docs

- Top-level `README.md` (Quick Start)
- `ARCHITECTURE.md`
