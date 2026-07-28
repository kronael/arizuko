---
status: shipped
---

# compose: native Docker Compose profiles and includes

Replace `compose.go`'s custom PROFILE system and TOML mini-format with
Docker Compose built-in machinery: `profiles:` for optional daemons,
`include:` for channel adapters, thin `arizuko packages` wrapper.

## Today's state

- `compose/compose.go` (~900 LOC) builds docker-compose.yml by hand.
- Optional core daemons (timed, onbod, davd) gated by Go conditionals
  keyed off `PROFILE=minimal|web|full` env var baked at code-generation
  time. Three hardcoded code paths.
- Channel adapters dropped as custom TOML files (`template/services/*.toml`),
  parsed line-by-line by `compose.go`, transformed to YAML by hand. Custom
  mini-format: per-adapter env, `[[proxyd_route]]` blocks, `gated_by` conditions.
- Generated compose.yml embeds all service definitions inline; no composition.

## Target shape

- Core daemons (`timed`, `onbod`, `davd`) get `profiles: ["timed"]` etc. in
  their compose stanzas. Generated compose.yml includes all three, but Docker
  only brings them up when `COMPOSE_PROFILES=timed,onbod` is set or
  `docker compose --profile timed up` is used. No Go conditionals.
- Channel adapters become native `.yml` fragments in `template/services/`.
  Each `teled.yml`, `slakd.yml`, etc. is a valid partial compose service.
  Generated compose.yml writes `include:` entries: `include: - ./services/teled.yml`.
  Docker handles the rest — no TOML parsing, no per-adapter Go functions.
- New `arizuko packages` subcommand:
  - `arizuko packages list` — available (in template/) vs enabled (in datadir/).
  - `arizuko packages add <name>` — copies fragment from template/ to datadir/services/.
  - `arizuko packages remove <name>` — deletes from datadir/services/.
- `compose.Generate()` shrinks to ~200 LOC: base service definitions
  (authd/routd/runed/proxyd/webd/vited), static include paths for enabled
  adapters, `PROXYD_ROUTES_JSON` from `template/services/*-routes.toml`.

## Migration

1. **Convert `template/services/*.toml` to `.yml` fragments** — one per
   channel adapter. Each becomes a valid partial compose service (image, env,
   ports, healthcheck). No TOML parsing; Docker's YAML loader is canonical.
   Move `[[proxyd_route]]` → `template/services/*-routes.toml` (or keep as
   comment in adapter .yml, evaluated by compose.go at include-time).

2. **Move PROFILE conditionals to `profiles:`** — timed/onbod/davd get
   `profiles: ["<name>"]` in generated compose.yml or in core-services.yml.
   No Go `if profile == "full"`.

3. **Rewrite compose.Generate()** — delete TOML parser, per-adapter service
   functions. Write base service defs; emit `include:` for each enabled adapter
   fragment; extract routes from template (if kept there) or parse from
   richer .yml schema.

4. **CLI wrapper `arizuko packages`** — read `template/services/`, list
   available; read `<datadir>/services/`, list enabled; `add`/`remove` copy/delete.

## What deletes

- `compose/compose.go` lines ~200-400: TOML parser, `ParseServicesTOML()`,
  per-adapter field extraction, `gated_by` filtering.
- `compose/compose.go:450-550` (approx): `timedService()`, `onbodService()`,
  `davdService()`, `davdProxyRoute()`, other per-daemon builder functions.
- `compose/compose.go` PROFILE conditionals (~30 lines scattered).
- `template/services/*.toml` TOML files; replaced by `.yml`.
- Custom env-var gating for routes; Docker profile selection is the single
  gate.

## Code pointers

- `compose/compose.go` — main target for shrinkage.
- `template/services/` — source repository; convert all .toml to .yml.
- `cmd/arizuko/packages.go` (new) — list/add/remove verbs.
- `template/services/timed.yml`, `onbod.yml`, `davd.yml` (or one
  `core-services.yml`) — where profiles land.
