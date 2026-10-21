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

```
┌──────────────────────────────┐   ┌──────────────────────────────┐
│ catalog: template/services/  │   │ source: git URL or local dir │
│ <name>.yml, <name>.yaml      │   │ *.yml, *.yaml, skills/       │
└──────────────┬───────────────┘   └──────────────┬───────────────┘
               │ packages add <name>              │ packages install <src>
               │ files linked                     │ files copied
               └─────────────────┬────────────────┘
                                 ▼
┌──────────────────────────────────────────────────────────────┐
│ installPackage: every check before the first write           │
│ 1 parsePkgManifests: rows parse, grants inside --folder,     │
│   no core route path, owner stamped package:<name>           │
│ 2 checkSourcedFiles: install only, the fragment stays in     │
│   its own namespace                                          │
│ 3 provisionPkgEnv: generate, default, required against .env  │
│ 4 files into <data>/services/                                │
│ 5 rows applied live through apply's engine, all or none      │
│ 6 skills/ into <data>/skills/                                │
│ 7 installed_packages row (folder '', name)                   │
└────────────────────────────────┬─────────────────────────────┘
                                 │ upgrade, sync, remove read the row back
                                 ▼
┌──────────────────────────────────────────────────────────────┐
│ arizuko generate                                             │
│ relink catalog copies, then compose.Generate: one include:   │
│ per fragment and COMPOSE_PROFILES; write proxyd_routes rows; │
│ copy package skills into every group; write                  │
│ docker-compose.yml                                           │
└────────────────────────────────┬─────────────────────────────┘
                                 ▼
                          docker compose up

products apply <folder>: the group-scoped half. groups/<folder>/products.toml
is blended into that group, one installed_packages row per (folder, product).
```

<!-- /twin -->

Sources: `cmd/arizuko/packages.go:136` `installPackage`,
`cmd/arizuko/main.go:157` `generateCompose`, `cmd/arizuko/products.go:89`
`applyProductMix`. The operator how-to is `EXTENDING.md` § Installing
packages; the command contract is `cmd/arizuko/README.md`.

A package ships two file kinds, split by the owner of each format.
`services/<name>.yml` is a partial compose file, docker's format, and
docker includes it verbatim; `compose.go` never parses it. The optional
`services/<name>.yaml` is arizuko's config manifest: its rows go through
`arizuko apply`'s engine, and this package reads its `proxyd_routes` and
`env` sections. One file that knew both formats would couple two schemas.
Adding an adapter touches no Go. Fragment paths resolve against
`services/`, so a fragment reads `../env/<name>.env`:

```yaml
services:
  teled:
    container_name: ${APP}_teled_${FLAVOR}
    image: arizuko:latest
    entrypoint: ['teled']
    env_file: ['../env/teled.env'] # relative to services/
    environment:
      ROUTER_URL: 'http://routd:8080'
    depends_on: [routd]
    restart: on-failure
```

The bundled catalog at `template/services/` holds a `<name>.yml` fragment
and a `<name>.yaml` manifest for each of `bskyd`, `discd`, `emaid`,
`extenddb`, `gitd`, `linkd`, `litellm`, `slakd`, `teled`, `ttsd`,
`twitd` and `whapd`, plus `kokoro.yml` alone and the variant
`teled-jynx.yml`.

A fragment from a source passes `CheckSourcedFragment` before a byte is
written. This applies to `arizuko packages <inst> install <dir|url>` and to
every `upgrade` or `sync` of it. Its services carry the package's name, it
binds only `${DATA_DIR}/store/<name>` or a named volume, it declares no
`env_file`, and it escapes no namespace: no `privileged`, `pid`,
`network_mode: host`, volume `driver_opts` or config `file:`. `docker
compose` refuses none of that, so a package could otherwise mount an
owner's store, `.env` or the docker socket and install cleanly (BUGS L201).
The release's own catalog is exempt: `fragments_test.go` holds its
fragments to the same rules at build.

`Generate` provisions each package's `env` section before it writes any
env file (`ProvisionPackageEnv`). A `.env` value wins. `generate: true`
mints a token once and appends it to `.env` above the managed block.
`default` fills the run's env only. `required` fails `generate` and names
the key. `Generate` reads a linked manifest from the readable catalog
(`CatalogDir`): the deploy unit runs `generate` in a container that mounts
the data dir and not the checkout, where the link dangles.

`services/<name>.yml` is a symlink at the bundled catalog
(`template/services/<name>.yml`), not a copy. `arizuko packages <inst> add
<name>` creates the link, and `arizuko generate` relinks an installed copy
that is still byte-identical to the catalog (`compose.RelinkCatalog`, BUGS
R1). A catalog fix then reaches every instance on its next generate, with
no copy to fall behind. The link target is `HOST_APP_DIR/template/services/`
(`compose.HostCatalogDir`), a host path, because `docker compose` reads
`include:` on the host even when `arizuko generate` ran inside the deploy
unit's container. Without `HOST_APP_DIR`, `packages add` writes a copy and
`generate` never relinks.

A fragment stays a real file, never linked and never touched again, in two
cases. A fragment that differs from the catalog by one byte is an operator
edit, because `packages add` writes the catalog verbatim. A fragment whose
name is a multi-account variant, § Several accounts on one platform, must
stay a file, because a link at the base template collides two services on
one `container_name`. `RelinkCatalog` returns the diverged set, and the
caller reports it: a diverged file is also what an older generator's output
looks like, and that instance runs a frozen copy (BUGS L41).

Third-party fragments, `kokoro.yml` and `litellm.yml`, never read the
shared `.env`. kokoro gets a generated `env/kokoro.env` that holds its one
key. litellm and its `litellm_db` have no env file: docker interpolates the
keys the fragment names, `${MODEL_GATEWAY_KEY:?}`,
`${MODEL_GATEWAY_DB_PASSWORD:?}` and the credential pair, from `.env`.
`compose/fragments_test.go` pins that set, so a new instance value for
either image is a visible edit. `packages add` mints both secrets into
`.env` from the `env` section of `litellm.yaml`, so the `:?` guards a
hand-copied fragment and is no operator step.

Routes stay out of the fragment, because proxyd, not docker, reads them.
`collectProxydRoutes` merges the core slice with each package manifest's
routes at generate time. `Generate` returns the result, and `arizuko
generate` writes it into `proxyd_routes` before it writes the compose file.

## Public API

- `Generate(dataDir string) (string, []ProxydRoute, error)` — the compose
  YAML plus the proxyd route table the same run decided on
- `ProxydRoute` — one route of a `<name>.yaml` manifest
- `ReadManifest(path) (Manifest, error)` — the `proxyd_routes` and `env`
  sections of one package manifest, parsed by `resreg.ParseManifest`;
  `ManifestRoutes(path)` is its routes half
- `ProvisionPackageEnv(dataDir, manifestPath, name, env)` — resolve a
  package's `env` section against `.env`; `packages add|install` and
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
- `manifest.go` — the `<name>.yaml` half of a package: its `proxyd_routes`
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
