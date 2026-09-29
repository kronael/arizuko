# compose

`docker-compose.yml` generator.

## Purpose

Emits the core daemon stanzas plus one `include:` per package fragment
in `<dataDir>/services/`, and provisions what a static file cannot:
per-daemon service keys, scoped `env/<daemon>.env` files, proxyd's
route table, and the egress name derivations.

Optional daemons are never gated by a Go conditional: every stanza is
always emitted with `profiles:` (`web` for webd/proxyd/vited/dashd, then
`timed`, `onbod`, `davd`), and docker starts the ones listed in
`COMPOSE_PROFILES` (`activeProfiles`, `compose.go:602`).

## The compose-managed .env block

`Generate` rewrites one block in `<dataDir>/.env`, leaving operator
lines untouched:

```
# --- compose-managed (do not edit) ---
APP=arizuko
FLAVOR=krons
DATA_DIR=/srv/data/arizuko_krons
COMPOSE_PROFILES=davd,onbod,timed,web
# --- end compose-managed ---
```

Docker interpolates package fragments with these — `${APP}`/`${FLAVOR}`
build the container name, `${DATA_DIR}` the host mount. `COMPOSE_PROFILES`
is derived from the feature flags (`WEB_PORT`, `WEBDAV_ENABLED`,
`ONBOARDING_ENABLED`); change a flag, regenerate.

## Packages

A package is `services/<name>.yml` (a partial compose file defining one
service) plus an optional `services/<name>.yaml` (its 5/8 manifest, whose
`proxyd_routes` and `env` blocks this package reads). Fragment paths
resolve against `services/`, so a fragment reads `../env/<name>.env`.

A fragment from a source — `arizuko packages <inst> install <dir|url>`,
and every `upgrade`/`sync` of it — passes `CheckSourcedFragment` before a
byte is written: its services carry the package's name, it binds only
`${DATA_DIR}/store/<name>` or a named volume, declares no `env_file`, and
escapes no namespace (`privileged`, `pid`, `network_mode: host`, a volume
`driver_opts`, a config `file:` …). `docker compose` refuses none of that,
so a package could otherwise mount an owner's store, `.env` or the docker
socket and install cleanly (BUGS L201). The release's own catalog is exempt:
its fragments are held to the tests in `fragments_test.go` at build.

`Generate` provisions each package's `env` block before it writes any env
file (`ProvisionPackageEnv`): a `.env` value wins; `generate` mints a token
once and appends it to `.env` above the managed block; `default` fills the
run's env only; `required` fails generate, naming the key. A linked manifest
is read from the readable catalog (`CatalogDir`), because the deploy unit
runs generate in a container that mounts the data dir and not the checkout,
where the link is dangling.

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

Routes stay out of the fragment on purpose: proxyd, not docker, is their
consumer, so `collectProxydRoutes` merges the core slice with each
package's routes file at generate time and `Generate` hands the result back
for `arizuko generate` to write into `proxyd_routes`.

## Public API

- `Generate(dataDir string) (string, []ProxydRoute, error)` — the compose
  YAML plus the proxyd route table the same run decided on
- `ProxydRoute` — one route of a `<name>.yaml` manifest
- `ReadManifest(path) (Manifest, error)` — the `proxyd_routes` and `env`
  blocks of one package manifest, parsed by `resreg.ParseManifest`;
  `ManifestRoutes(path)` is its routes half
- `ProvisionPackageEnv(dataDir, manifestPath, name, env)` — resolve a
  package's `env` block against `.env`; `packages add|install` and
  `Generate` share it
- `CatalogDir(hostAppDir)` — the catalog this process can read: the
  checkout when visible, else the copy baked into the image
- `HostCatalogDir(dataDir) (string, bool)` — the host-resolvable catalog
  dir from `.env`'s `HOST_APP_DIR`, for use as a symlink target
- `RelinkCatalog(servicesDir, readableCatalogDir, hostCatalogDir)
([]string, error)` — convert every installed fragment that is still a
  byte-identical catalog copy into a symlink, idempotently
- `SymlinkFragment(servicesDir, hostCatalogDir, filename) error` —
  atomically point one fragment (or routes sidecar) at the catalog
- `ReservedRoutePaths() []string` — the release's own `/dash/*` and `/v1/*`
  prefixes. dashd's hub falls back to these when proxyd cannot be read, so an
  operator is never linked at a path that 404s
- `MigrateStoreLayout(storeDir) ([]string, error)` — moves an instance's owner
  files into `store/<owner>/`, each with its `-wal` and `-shm` siblings, and
  answers what it moved. Runs on the HOST and nowhere else: the narrowing it
  enables is what removes the flat path from a daemon's own mount, so nothing
  can migrate itself

## Dependencies

- `core`, `resreg` (manifest parsing), `store` (the owner layout)

## Files

- `compose.go` — the generator: every daemon's service block, the scoped env
  files, and the `services/*.yml` fragments it includes. `.yml` is the only
  package format.
- `manifest.go` — the `<name>.yaml` half of a package: its `proxyd_routes`
  and `env` blocks, and the catalog they are read from.
- `store_layout.go` — `MigrateStoreLayout`, the host-side owner-file move.
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
