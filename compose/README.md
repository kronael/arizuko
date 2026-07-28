# compose

`docker-compose.yml` generator.

## Purpose

Emits the core daemon stanzas plus one `include:` per package fragment
in `<dataDir>/services/`, and provisions what a static file cannot:
per-daemon service keys, scoped `env/<daemon>.env` files, proxyd's
route table, and the egress name derivations.

Optional daemons are never gated by a Go conditional: every stanza is
always emitted with `profiles:` (`web` for webd/proxyd/vited/dashd, then
`timed`, `onbod`, `davd`, `crackbox`), and docker starts the ones listed
in `COMPOSE_PROFILES`.

## The compose-managed .env block

`Generate` rewrites one block in `<dataDir>/.env`, leaving operator
lines untouched:

```
# --- compose-managed (do not edit) ---
APP=arizuko
FLAVOR=krons
DATA_DIR=/srv/data/arizuko_krons
COMPOSE_PROFILES=crackbox,davd,onbod,timed,web
# --- end compose-managed ---
```

Docker interpolates package fragments with these — `${APP}`/`${FLAVOR}`
build the container name, `${DATA_DIR}` the host mount. `COMPOSE_PROFILES`
is derived from the feature flags (`WEB_PORT`, `WEBDAV_ENABLED`,
`ONBOARDING_ENABLED`, `CRACKBOX_ADMIN_API`); change a flag, regenerate.

## Packages

A package is `services/<name>.yml` (a partial compose file defining one
service) plus an optional `services/<name>-routes.json`. Fragment paths
resolve against `services/`, so a fragment reads `../env/<name>.env`.
Multi-account adapters (`specs/5/R-multi-account.md`) drop in as
`<adapter>-<label>.yml` (e.g. `teled-work.yml`) and reuse the base
adapter's scoped env file, so per-daemon secret isolation extends across
accounts.

Routes stay out of the fragment on purpose: proxyd takes ONE
`PROXYD_ROUTES_JSON` env var, so `collectProxydRoutes` merges the core
slice with each package's routes file at generate time.

## Public API

- `Generate(dataDir string) (string, error)` — returns the compose YAML
- `ProxydRoute` — one entry of a `<name>-routes.json`

## Dependencies

- `core`

## Files

- `compose.go`
- `legacy.go` — one-shot `services/*.toml` → `.yml` conversion for data
  dirs seeded before spec 5/27; delete once none remain

## Scoped env keys

Each daemon gets only the keys it needs, written to `env/<daemon>.env`
(`commonKeys` + `daemonKeys` in `compose.go`). Secrets a daemon is not
listed for never reach it. Shared secrets that cross a service boundary
must appear in both lists.

## Related docs

- `ARCHITECTURE.md` (Compose Containers)
- `../template/services/` — bundled packages
- `specs/5/27-compose-native-packaging.md`
