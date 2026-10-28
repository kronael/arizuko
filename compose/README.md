# compose

`docker-compose.yml` generator.

## Purpose

`Generate` emits the core daemon stanzas plus one `include:` per package
fragment in `<dataDir>/services/`. It also provisions what a static file
cannot hold: per-daemon service keys, scoped `env/<daemon>.env` files,
proxyd's route table and the egress name derivations.

## Profiles decide what starts

```
.env feature flags                     activeProfiles, compose.go:736
WEB_PORT set ────────────────────────► web
WEB_PORT set, WEBDAV_ENABLED=true ───► davd, unset counts as true
always ──────────────────────────────► timed
ONBOARDING_ENABLED=true ─────────────► onbod
                    │
                    │ writeManagedEnv
                    ▼
┌──────────────────────────────────────────────────────┐
│ compose-managed block in .env                        │
│ APP, FLAVOR, DATA_DIR, COMPOSE_PROFILES              │
└──────────────────────────┬───────────────────────────┘
                           │ docker interpolates and filters
                           ▼
┌──────────────────────────────────────────────────────┐
│ docker-compose.yml, every stanza always emitted      │
│ authd routd runed        no profile: always up       │
│ webd proxyd vited dashd  profile web                 │
│ timed, onbod, davd       a profile of its own name   │
│ include: ./services/<name>.yml, one per fragment     │
│                          no profile: present = up    │
└──────────────────────────┬───────────────────────────┘
                           │ docker compose up
                           ▼
containers <app>_<service>_<flavor>, e.g. arizuko_teled_krons
```

`Generate` writes every core stanza on every run, and `profiles:` decides
what docker starts (`writeSvc`, `compose.go:1135`). No Go conditional gates
an optional daemon. `authd`, `routd` and `runed` carry no profile, because
they are the three planes and always run. `webd`, `proxyd`, `vited` and
`dashd` share the `web` profile, because compose rejects a `depends_on` into
an inactive profile. `timed`, `onbod` and `davd` take their profile inline
from `compose.go`: they are core daemons, not includable packages.

`COMPOSE_PROFILES` is the single gate. A feature is off because its daemon
is not running or its env var is unset, never because a flag says `false`.
A boolean flag beside the profile list is a second gate, and a second gate
drifts from the first. An included fragment declares no profile, so a
package is on when its fragment is in `services/` and off when it is not.
A new profile is a new env
var read by `activeProfiles`, never a second switch.

Every container is named `<app>_<service>_<flavor>`. `Generate` reads `app`
and `flavor` from the data dir name and writes them into the managed block.
A daemon reads them from its env and never derives them from a path inside
its container.

Adapters reach the router at `ROUTER_URL=http://routd:8080`. `runed` is the
one built-in service that docker gives `/dev/kvm`, `/dev/vhost-vsock`,
`/dev/net/tun` and the `NET_ADMIN` and `SYS_ADMIN` capabilities, because it
boots the turn's KVM guest and its tap. `authd` serves JWKS, so every other
daemon verifies a token offline. The compose file holds no egress service:
`runed` starts one `egress` process per turn, `SECURITY.md` § Network egress
isolation.

All Go daemons listen on `:8080` inside the container, except `ttsd` at
`:8880`. The third-party images keep their own ports: kokoro `:8880`,
litellm `:4000`.

## A daemon mounts only what it opens

A daemon mounts only the data-dir subpaths it opens
(`compose_test.go` `wantDataMounts`), because the whole tree holds every
owner's files and every live MCP socket under `ipc/`. routd and runed take the
whole tree: routd reads `connectors.toml`, `surrogate/*.toml` and `tts/` from
the root and writes `audit-*.jl` there, and runed drives the spawn path. dashd
mounts `groups/` and `surrogate/`, onbod `groups/` and `web/`. authd, webd and
proxyd mount nothing.

## The compose-managed .env block

`Generate` rewrites one block in `<dataDir>/.env` and leaves the operator
lines untouched:

```
# --- compose-managed (do not edit) ---
APP=arizuko
FLAVOR=krons
DATA_DIR=/srv/data/arizuko_krons
COMPOSE_PROFILES=davd,onbod,timed,web
# --- end compose-managed ---
```

Docker interpolates package fragments with these values. `${APP}` and
`${FLAVOR}` build the container name, and `${DATA_DIR}` builds the host
mount. To change `COMPOSE_PROFILES`, change a feature flag and run
`arizuko generate` again. A malformed marker, such as a second BEGIN or a
BEGIN with no END, stops `generate`, because stripping it would drop every
operator line after it.

## Packages

<!-- twin:packages -->

```text
┌──────────────────────────────┐     ┌──────────────────────────┐
│ installed_packages rows      │     │ Local or Git source      │
│ from package add and rm      ├────►│ arizuko.yml and assets   │
└──────────────────────────────┘     └────────────┬─────────────┘
                                                  │ package sync
                                    ┌─────────────▼────────────┐
                                    │ Installed package assets │
                                    │ and owned resource rows  │
                                    └─────────────┬────────────┘
                                                  │ generate
                                    ┌─────────────▼────────────┐
                                    │ Services and compose     │
                                    └─────────────┬────────────┘
                                                  │ deployment
                                    ┌─────────────▼────────────┐
                                    │ Running services         │
                                    └──────────────────────────┘
```

<!-- /twin -->

[EXTENDING.md](../EXTENDING.md#installing-packages) owns package authoring,
selection, order, host builds and group assets. Compose owns service includes and
environment files. A group selection has no parent-directory inheritance.

The bundled service catalog has one directory per package:
`template/services/<name>/{arizuko.yml,<name>.yml}`.
Installed metadata lives at `<data>/packages/<name>/arizuko.yml`;
Docker includes `<data>/services/<name>.yml`.
Arizuko reads metadata through the shared strict resreg manifest decoder.
Docker reads its own compose format; compose generation does not translate it.

Source fragments pass `CheckSourcedFragment`: services stay inside their package
namespace, binds stay under `${DATA_DIR}/store/<name>` or named volumes, and
privileged containers, host networking, foreign env files and escaping mounts refuse.
The release catalog is checked by `fragments_test.go`.

Only installed service fragments participate in environment provisioning.
`ProvisionPackageEnv` reads their canonical manifest. A `.env` value wins;
`generate` mints once, `default` fills the current render, and `required` refuses
an unset value. The shared `.env` never becomes a service env file.
Group secret requirements remain package metadata; scoped delivery belongs to routd.

`RelinkCatalog` links an installed fragment only when its bytes match the current
catalog. Edited files and account variants stay files. The host resolves include
links through `HostCatalogDir`; the generator reads its catalog through `CatalogDir`.
The checkout and deployed release must match. Existing BUGS L202 records that coupling.

`collectProxydRoutes` reads core routes and the `proxyd_routes` rows from installed
package manifests. Generation evaluates `gated_by` against the configured environment,
then writes enabled routes through `SyncProxydRoutes`. A route takes effect without
a service restart. Starting changed containers remains the deployment procedure.

## Public API

- `Generate(dataDir string) (string, []ProxydRoute, error)` — the compose
  YAML plus the proxyd route table the same run decided on
- `ProxydRoute` — one route of an `arizuko.yml` manifest
- `ReadManifest(path) (Manifest, error)` — the `proxyd_routes` and `env`
  sections of one package manifest, parsed by `resreg.ParseManifest`;
  `ManifestRoutes(path)` is its routes half
- `ProvisionPackageEnv(dataDir, manifestPath, name, env)` — resolve a
  package's `env` section against `.env`; `package sync` and
  `Generate` share it
- `CatalogDir(hostAppDir)` — the catalog this process can read: the
  checkout when visible, else the copy baked into the image
- `HostCatalogDir(dataDir) (string, bool)` — the host-resolvable catalog
  dir from `.env`'s `HOST_APP_DIR`, for use as a symlink target
- `RelinkCatalog(servicesDir, readableCatalogDir, hostCatalogDir)
([]string, []Diverged, error)` — convert every installed fragment that is
  still a byte-identical catalog copy into a symlink, idempotently, and name
  every fragment kept as a real file
- `SymlinkFragment(servicesDir, hostCatalogDir, filename) error` —
  atomically point one fragment at the catalog
- `CheckSourcedFragment(name, file, body) error` — the namespace bound on a
  fragment from a source
- `ReservedRoutePaths() []string` — every path core arizuko serves, derived
  from `coreProxydRoutes`. A package may not claim one, and dashd's hub falls
  back to these when proxyd cannot be read
- `ReservedRouteClaim(bundled, path) string` — the core path `path` would
  take over; a bundled package keeps its own `/dash/<name>/`
- `CoreRouteAuth(path) string` — the auth mode core declares for one path

## Dependencies

- `core`, `resreg` (manifest parsing), `store` (the owner layout)

## Files

- `compose.go` — the generator: every daemon's service block, the scoped env
  files, the managed `.env` block and the `include:` list.
- `manifest.go` — the `arizuko.yml` half of a package: its `proxyd_routes`
  and `env` sections, and the catalog they are read from.
- `contain.go` — `CheckSourcedFragment`.
- `fragments.go` — `RelinkCatalog` and `SymlinkFragment`. Matching is by
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

### Several accounts on one platform

Two accounts on one platform are two services of one adapter binary, and
need no new code. The second account is its own fragment,
`<adapter>-<label>.yml`, for example `template/services/teled-jynx.yml`. It
sets its own `container_name`, `env_file`, `LISTEN_URL`, `CHANNEL_NAME` and
state mount (`variantOf`, `compose.go:263`). Only a name whose base is a
channel adapter is a variant.

The variant reads `env/<adapter>-<label>.env`. `writeEnvFile` fills it with
the base adapter's keys, and a non-empty `<KEY>_<LABEL>` line in `.env`
replaces `<KEY>`, so each account holds its own credential
(`TELEGRAM_BOT_TOKEN_JYNX` replaces `TELEGRAM_BOT_TOKEN`). An unset or empty
override keeps the base value. The label is
upper-cased and every other non-alphanumeric byte becomes `_`
(`variantKey`). Only the adapter's own `daemonKeys` take an override; the
common keys and the service key stay the base adapter's, so every account
signs as the same `service:<adapter>`. A variant with no non-empty
override line polls the same account as the base.
