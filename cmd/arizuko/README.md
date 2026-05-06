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
  - `arizuko chat <instance>` — interactive Claude Code session bound to root MCP socket

## Dependencies

- `compose`, `container`, `core`, `store`

## Files

- `main.go` — command dispatch, each `cmd*` function

## Related docs

- Top-level `README.md` (Quick Start)
- `ARCHITECTURE.md`
