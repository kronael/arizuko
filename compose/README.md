# compose

`docker-compose.yml` generator.

## Purpose

Builds the compose file from `.env` (profile + feature flags) plus any
TOML service files in `<dataDir>/services/`. Emits built-ins (gated,
timed, webd/proxyd/vited, dashd, davd, onbod) conditional on profile
and flags; appends operator-supplied adapter TOMLs.

Multi-account adapters (`specs/5/R-multi-account.md`) drop in as
`<adapter>-<label>.toml` (e.g. `teled-work.toml`); they reuse the base
adapter's scoped `env/<adapter>.env` so per-daemon secret isolation
extends across accounts.

## Public API

- `Generate(dataDir string) (string, error)` — writes `<dataDir>/docker-compose.yml`, returns path
- `ServiceConfig` — TOML service shape (`image`, `entrypoint`, `depends_on`, `volumes`, `[environment]`)

## Dependencies

- `core`

## Files

- `compose.go`

## Scoped env keys

Each daemon gets only the keys it needs, written to `env/<daemon>.env`.
Shared secrets that cross service boundaries must appear in both lists
(see `daemonEnvKeys` in `compose.go`). Key example: `PROXYD_HMAC_SECRET`
must reach both `proxyd` (signer) and `webd` / `onbod` (verifiers) — if
missing from either, signed-header verification silently breaks.

## Related docs

- `ARCHITECTURE.md` (Compose Containers)
- `../template/services/` — bundled adapter TOMLs
