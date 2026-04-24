# compose

`docker-compose.yml` generator.

## Purpose

Builds the compose file from `.env` (profile + feature flags) plus any
TOML service files in `<dataDir>/services/`. Emits built-ins (gated,
timed, webd/proxyd/vited, dashd, davd, onbod) conditional on profile
and flags; appends operator-supplied adapter TOMLs.

## Public API

- `Generate(dataDir string) (string, error)` — writes `<dataDir>/docker-compose.yml`, returns path
- `ServiceConfig` — TOML service shape (`image`, `entrypoint`, `depends_on`, `volumes`, `[environment]`)

## Dependencies

- `core`

## Files

- `compose.go`

## Related docs

- `ARCHITECTURE.md` (Compose Containers)
- `../template/services/` — bundled adapter TOMLs
