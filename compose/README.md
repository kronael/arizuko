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

`services/<name>.yml` is a SYMLINK at the bundled catalog
(`template/services/<name>.yml`), not a copy — `arizuko packages <inst>
add <name>` creates the link directly, and `arizuko generate` relinks any
older installed copy that is still byte-identical to the catalog
(`compose.RelinkCatalog`, BUGS R1). A catalog fix then reaches every
instance on its next generate/deploy; there is no copy to fall behind and
nothing to sync. The symlink target is `HOST_APP_DIR/template/services/`
(`compose.HostCatalogDir`) — a HOST-resolvable path, because `docker
compose` always reads `include:` on the bare host, including when
`arizuko generate` itself ran inside the ephemeral container the deploy
systemd unit spawns for it. Without `HOST_APP_DIR` set, `packages add`
falls back to a plain copy (pre-fix behavior) and generate never relinks.

A fragment stays a REAL file — never linked, never touched again — when
it diverges from the catalog by even a byte (a hand edit; `packages add`
writes the catalog verbatim, so anything else means the operator changed
it) or when its filename encodes a multi-account variant
(`specs/5/R-multi-account.md`): `<adapter>-<label>.yml` (e.g.
`teled-work.yml`) carries its own `container_name`/env and reuses the
base adapter's scoped env file, so per-daemon secret isolation extends
across accounts. Converting it to a symlink at the base template would
collide two services on one `container_name`.

Routes stay out of the fragment on purpose: proxyd takes ONE
`PROXYD_ROUTES_JSON` env var, so `collectProxydRoutes` merges the core
slice with each package's routes file at generate time.

## Public API

- `Generate(dataDir string) (string, error)` — returns the compose YAML
- `ProxydRoute` — one entry of a `<name>-routes.json`
- `HostCatalogDir(dataDir) (string, bool)` — the host-resolvable catalog
  dir from `.env`'s `HOST_APP_DIR`, for use as a symlink target
- `RelinkCatalog(servicesDir, readableCatalogDir, hostCatalogDir)
([]string, error)` — convert every installed fragment that is still a
  byte-identical catalog copy into a symlink, idempotently
- `SymlinkFragment(servicesDir, hostCatalogDir, filename) error` —
  atomically point one fragment (or routes sidecar) at the catalog

## Dependencies

- `core`

## Files

- `compose.go` — reads `services/*.yml` fragments; `.yml` is the only
  package format (the pre-5/27 `.toml` converter was removed once every
  live data dir was on `.yml`).
- `fragments.go` — `RelinkCatalog` converts an installed copy that is still
  byte-identical to `template/services/` into a symlink there, so a catalog
  fix reaches every instance with no copy to fall behind. Matching is by
  service kind, not filename: `teled-rhias.yml` maps to the `teled`
  template, and because its filename says "variant" it is never linked.

## Scoped env keys

Each daemon gets only the keys it needs, written to `env/<daemon>.env`
(`commonKeys` + `daemonKeys` in `compose.go`). Secrets a daemon is not
listed for never reach it. Shared secrets that cross a service boundary
must appear in both lists. **No fragment may read the shared `.env`** —
that file holds `SECRETS_KEY`, `AUTH_SECRET`, `GITHUB_CLIENT_SECRET`,
`CLAUDE_CODE_OAUTH_TOKEN` and every bot token.

A service running an image arizuko does not build is listed in
`foreignImages` and gets `daemonKeys` only, no `commonKeys`: those are
what every _arizuko_ daemon reads, and they include
`OTEL_EXPORTER_OTLP_HEADERS` (the collector's auth token) plus the host's
filesystem layout.

## Related docs

- `ARCHITECTURE.md` (Compose Containers)
- `../template/services/` — bundled packages
- `specs/5/27-compose-native-packaging.md`
