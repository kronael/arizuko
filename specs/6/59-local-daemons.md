---
status: draft
---

# specs/6/59 — one package contract for local daemons

A package supplies files, skills or a service through arizuko's existing
install lifecycle. Every core daemon uses that contract too.
Package identity does not require a separate process.
[7/26-single-world-deployment](../7/26-single-world-deployment.md) owns consolidation.

## Core wiring prevents outside packages from joining

The existing lifecycle owns sources, revisions, assets and resource rows:
[EXTENDING.md](../../EXTENDING.md#installing-packages).
Name-based wiring remains in `compose/compose.go:99`, `authd/http.go:27`,
`cmd/arizuko/packages.go:38` and `dashd/services.go:43`; BUGS `L200` records it.
The L199 regression boundary is apply every declared row or refuse the install.
L199 has no current standalone entry in `BUGS.md`.

Socket discovery is proposed. Adapters register prefixes and capabilities at
`routd/channels.go:58`. `resreg.OpenAPIHandler` describes mounted resources at
`resreg/openapi.go:505`; it does not describe channel identity or every handler.

## Compose runs packages; routd discovers their sockets

```text
package source (changed) ──► existing installer (changed) ──► Compose
                                                               │ start
                                                               ▼
routd (changed) ◄── HTTP/socket discovery (new) ──────── daemon (changed)
                                                               │ signed requests
                                                               ▼
                                                          shared store
```

Compose owns commands, images, mounts, dependencies and restarts.
The existing systemd unit owns the Compose project.
There is no D-Bus, new registry service, installer, broker or supervisor.
The socket directory supplies addresses, never authority.

## Module map

Catalog patterns mean one manifest and fragment per service, using the same
parser and renderer. Core includes authd, routd, runed, proxyd, webd, dashd,
onbod and davd until an approved fold removes a process.
The vited and timed removals belong to 7/26.

```yaml
- path: resreg/engine.go (changed)
  job: parse service metadata beside existing package metadata
  names: [Manifest, ParseDeclaration, EnvNeed]
  never: start services or approve permissions
- path: compose/manifest.go (changed)
  job: read the same declaration and build the enrollment view before services start
  names: [ReadManifest, ProvisionPackageEnv]
  never: duplicate routes or credentials in sidecars
- path: compose/compose.go (changed)
  job: render all services from installed artifacts
  names: [Generate, WriteStoreKey]
  never: select behavior through daemon-name tables
- path: cmd/arizuko/sync.go (changed)
  job: use one install and removal path for all packages
  names: []
  never: report success for ignored rows
- path: cmd/arizuko/packages.go (changed)
  job: validate artifact ownership and Compose dependencies
  names: []
  never: keep baseDaemons as a second core list
- path: cmd/arizuko/main.go (changed)
  job: materialize required packages before database-backed selection
  names: []
  never: run a second installer
- path: cmd/arizuko/store.go (changed)
  job: enroll selected services through the existing policy and key writer
  names: []
  never: grant every package all tables
- path: store/owner_reach.go (changed)
  job: derive enrollment reach from approved declarations
  names: [StoreReach]
  never: retain a hand-maintained duplicate map
- path: store/owner_reach_test.go (changed)
  job: compare core declarations with actual store callers
  names: [TestStoreReachMatchesTheCallSites]
  never: infer authority from directory names
- path: authd/http.go (changed)
  job: read generated scopes through the existing service-token exchange
  names: []
  never: trust scopes requested by a daemon
- path: resreg/resreg.go (changed)
  job: share the existing MCP descriptor renderer with OpenAPI emission
  names: [MCPTool, MCPTools]
  never: derive tool names twice
- path: resreg/openapi.go (changed)
  job: emit optional channel metadata beside mounted resources
  names: [OpenAPIHandler, MountedResources]
  never: advertise another mux's resources
- path: chanlib/handler.go (changed)
  job: expose channel metadata from existing live values
  names: [NewAdapterMux, CapImplReport]
  never: keep a second capability declaration
- path: chanlib/chanlib.go (changed)
  job: use socket transport and remove active registration
  names: [RouterClient]
  never: bypass authorization on local calls
- path: chanreg/chanreg.go (changed)
  job: refresh the existing channel view from admitted discoveries
  names: [Registry, Entry]
  never: persist a service registry
- path: chanreg/health.go (changed)
  job: reconcile sockets at startup and during existing health checks
  names: [StartHealthLoop]
  never: equate a socket inode with a live daemon
- path: chanreg/httpchan.go (changed)
  job: send existing verbs through the shared socket transport
  names: [HTTPChannel]
  never: define another channel protocol
- path: routd/channels.go (changed)
  job: keep the read surface and remove registration writes
  names: [SetChannelRegistry]
  never: accept self-asserted identity
- path: routd/discovery.go (new)
  job: reconcile installed endpoints and translate their OpenAPI descriptions
  names: []
  never: trust discovered metadata as authority
- path: routd/mcp.go (changed)
  job: expose discovered operations through the existing turn gate
  names: [Server]
  never: grant a tool merely because it was discovered
- path: dashd/services.go (changed)
  job: derive tiles from installed routes and discovered health
  names: []
  never: keep a static daemon list
- path: auth/transport.go (new)
  job: share HTTP dialing and listening on configured sockets
  names: [Listen, HTTPClient]
  never: replace authn or authz
- path: ant/arizuko.yml (changed)
  job: declare the core asset package as release-required
  names: [name, required]
  never: start an agent inside a daemon process
- path: template/services/<daemon>/arizuko.yml (new for core; changed for adapters)
  job: declare identity, environment, scopes, store reach and owned rows
  names: [name, env, service, proxyd_routes]
  never: repeat Docker command or mount syntax
- path: template/services/<daemon>/<daemon>.yml (new for core; changed for adapters)
  job: run the executable with approved mounts and socket access
  names: [command, image, depends_on, volumes]
  never: pass the shared instance env file
- path: routd/cmd/routd/main.go (changed)
  job: bind the configured socket and start discovery
  names: [LISTEN_ADDR]
  never: derive identity from the socket path
- path: runed/cmd/runed/main.go (changed)
  job: bind the configured runtime service socket
  names: [LISTEN_ADDR]
  never: expose the service directory to a guest
- path: authd/main.go (changed)
  job: bind the configured issuer socket
  names: [LISTEN_ADDR]
  never: depend on routd to mint bootstrap tokens
- path: proxyd/main.go (changed)
  job: serve local management over the socket and retain public TCP ingress
  names: [LISTEN_ADDR]
  never: duplicate handlers between listeners
- path: onbod/main.go (changed)
  job: bind the configured admission socket
  names: [LISTEN_ADDR]
  never: move table ownership
- path: dashd/main.go (changed)
  job: use the configured listener and backend addresses
  names: [LISTEN_ADDR]
  never: infer backend identity from paths
- path: webd/main.go (changed)
  job: use the shared socket listener for the existing server
  names: [LISTEN_ADDR]
  never: replace the web channel contract
- path: auth/service.go (changed)
  job: exchange service tokens over the shared transport
  names: [ServiceToken]
  never: change token authority
- path: runed/api/v1/client.go (changed)
  job: dispatch through the configured runtime endpoint
  names: [Client]
  never: widen the execution port
- path: cmd/arizuko/packages_install_report_test.go (changed)
  job: keep the L199 apply-or-refuse regression boundary
  names: []
  never: accept ignored declarations
- path: cmd/arizuko/minimal_package_test.go (changed)
  job: exercise an outside adapter and resource service
  names: []
  never: add fixture names to production maps
- path: EXTENDING.md (changed)
  job: document the completed package and service contract
  names: [Installing packages]
  never: offer a second install path
```

## One declaration, one lifecycle

```text
arizuko.yml ── parse (changed) ──► resreg.Manifest (changed)
                                          │
                      ┌───────────────────┴─────────────────────┐
                      ▼                                         ▼
            existing row/asset writers               generation/enrollment
                                                     readers (changed)
```

Current metadata at `resreg/engine.go:1034-1041`:

```go
type Manifest struct {
    Name      string             `yaml:"name,omitempty"`
    Brand     string             `yaml:"brand,omitempty"`
    Tagline   string             `yaml:"tagline,omitempty"`
    Install   string             `yaml:"install,omitempty"`
    Env       map[string]EnvNeed `yaml:"env,omitempty"`
    Documents []ManifestDocument `yaml:"-"`
}
```

Proposed additions to that type, subject to open question 1:

```go
Required bool                `yaml:"required,omitempty"`
Service  *ServiceDeclaration `yaml:"service,omitempty"`
```

`service` has an explicit `name`, an `env_file` key list, requested service-token
`scopes`, and requested `store` owner prefixes. Empty scopes or reach grant nothing.
`env` keeps its provisioning meaning: generate, default or require a value.
Routes remain typed `proxyd_routes` rows. Docker reads the separate fragment.
No manifest `run`, `image` or shorthand `routes` duplicates those formats.
The package's run command is Compose's `command` or `entrypoint`.

The existing `install` command can use `go install` with `GOBIN="$PREFIX/bin"`.
The fragment runs the installed binary in a compatible image.
A repository, archive or local directory uses the existing source resolver.
Builds run at installation, never at daemon startup or during agent turns.
No new distro is required; the package chooses an image matching its binary.

Core packages may share the release image. Each remaining daemon owns a package
manifest and fragment. A folded function belongs to its host package, without
an empty package that pretends to remain a daemon.
Compose `depends_on` owns service ordering and dependency validation.

Installation is an operator action that approves service scopes, store reach
and host mounts. A group-scoped selection cannot provision an instance service
or acquire instance credentials. Packages cannot install ACL rows.
Generated env files contain only approved keys and enrolled credentials.
Preserve runed's `store.EnvProfileKeys` projection and exclusion of `SECRETS_KEY`.
Multi-account adapters retain explicit identities and scoped env files.

`installed_packages` remains the selection and ownership record.
Updates validate rows, dependencies and edited artifacts before replacement.
Failures report completed effects and leave the selection retryable.
There is no transaction across files, services and multiple owners.
Removal stops owned services, withdraws owned rows and revokes service credentials.
It preserves unrelated rows, group files and stored application data.

Skill order is core first, then selected packages by installation order, filtered
by instance and exact group (`routd/packages_store.go:158`).
Writable group-local skills and disabled markers remain supported.
`container.ReadPackageInventory` and runed's projection remain the shared source
for harness views (`container/packages.go:190`, `runed/mounts.go:43`).
A listing budget may omit a summary, never access to the installed skill.
Foreign package imports translate into this representation and report unsupported
capabilities. No import adapter or retrieval system is required without a
concrete package or measured lookup failure.

## Bootstrap and authority use existing services

```text
required files (changed) ──► store enroll (changed) ──► authd
                                                        │ service tokens
                                                        ▼
optional rows ◄── existing resource writers ◄───────── routd
                                                        │ isolated execution
                                                        ▼
                                                      runed
```

Current enrollment at `cmd/arizuko/store.go:177-178`:

```go
for _, daemon := range slices.Sorted(maps.Keys(store.StoreReach)) {
    prefixes := store.StoreReach[daemon]
```

Proposed: generation derives `store.StoreReach` from approved installed
manifests before this loop. The declaration parser stays outside `store` to
avoid an import cycle: resreg already imports store.
Keep the policy and key writer at `cmd/arizuko/store.go:126` and
`cmd/arizuko/store.go:171`.
Existing cross-owner reach stays explicit (`store/owner_reach.go:16`).
In particular, onbod, dashd, webd and proxyd need more than their own prefix.
Keep `TestStoreReachMatchesTheCallSites` against the new declaration source.
[6/43-shared-store-server](43-shared-store-server.md) owns the external server
and account model. `objstore` stays `Query` and `GuardedBatch`.

The existing installer first materializes required release packages without builds.
Enrollment supplies scoped keys; generation supplies service identities.
authd prepares its own store and exposes its key and token surfaces.
Its bootstrap scopes come from approved generated declarations, so token exchange
does not wait for routd's grants API.
routd prepares its store and records required selections through the same package
writers. Each later owner prepares its own tables before package rows apply.
Dependency failure prevents dependent readiness. Startup retries reconcile effects
without a second install ledger. Open question 1 defines the initial selection.

All local requests retain service tokens and the existing authn/authz middleware.
Keep proxyd transit checks and per-operation scope and folder checks.
Cold resources keep one resreg handler for REST and MCP.
Agent tools use `AuthorizeTurn` with the resolved target.
OpenAPI is public metadata, never authority to invoke an operation.

The asset package `core` stays first and required (`container/packages.go:243`).
runed mounts selected assets into the isolated guest (`runed/mounts.go:63`).
The daemon socket directory never enters that guest; per-turn MCP stays separate.
`runed.Runtime` stays `Run` and `Kill` (`runed/runtime.go:32`).
[7/57-group-handler](../7/57-group-handler.md) owns handler selection;
[7/58-group-posts](../7/58-group-posts.md) owns group messaging.

## Sockets locate services; OpenAPI describes capabilities

```text
routd discovery (new) ── GET openapi.json (new) ──► daemon socket (new)
          │ reconcile (changed)
          ▼
existing chanreg / per-turn MCP view (changed)
registration writes (removed)
```

Current registration sends `name`, `url`, `jid_prefixes` and `capabilities`
at `chanlib/chanlib.go:163`. Proposed: the same live channel values appear in
an `x-arizuko-channel` extension of the engine-emitted OpenAPI document.
The socket supplies the address; advertised URLs cannot redirect discovery.
Identity comes from the installed declaration, not the response.
The extension shares its inputs with health and the dashboard.
Generic resource services need no channel extension.

Extend `resreg.OpenAPIHandler` with optional channel metadata from the adapter's
existing constructor values. Keep mounted-resource reflection unchanged.
Adapter verbs retain their existing protocol and capability semantics.
[6/31-adapter-contract](31-adapter-contract.md) owns the dashboard, not that protocol.
Non-Go adapters implement the same OpenAPI extension and HTTP contract.

Each instance has one private socket directory, mounted as `/run/arizuko`.
Generation configures addresses through existing listener and URL settings.
Use `unix:///run/arizuko/<service>/http.sock` in existing URL fields.
`LISTEN_ADDR` accepts the same socket form. proxyd retains its existing published
TCP port and mounts the same handler on its local management socket.
No daemon derives identity from a path. All clients use the shared HTTP transport.
Standalone deployments retain TCP.

Mount the directory read-only for clients, with only the producer's own
service subdirectory mounted writable. The operator creates those subdirectories.
This works with shared container UIDs and prevents replacing peer sockets.
A producer removes only its own stale socket after confirming no listener exists.
Only installed service identities enter discovery.
routd enumerates at startup and reconciles during its existing health loop.
Required core readiness fails when its declared endpoint cannot be discovered.
A bounded connection and valid document establish availability.
Stale inodes, failed health checks and missing sockets withdraw live entries.
Reappearance refreshes the cached document. Duplicate service identities,
resource names or JID-prefix claims fail without replacing their owner.

The existing chanreg remains the channel view, not a second registry.
Discovered `x-mcp-*` operations enter the existing turn view with schemas and gates.
Open question 3 covers missing metadata. `x-mcp-when` alone is insufficient.
Discovery neither probes paths to invent handlers nor requires core Go edits
for a new resource. Dashboard tiles derive from owned dashboard routes and the
same health state. An installed but stopped service remains visible as unavailable;
package removal withdraws its routes and tile.
Remove registration and deregistration writers when this covers all adapters.
No parallel registration mechanism remains.

## Publishing boundaries

[6/16-daemon-standalone-matrix](16-daemon-standalone-matrix.md) supplies the evidence.
`mcpfw`, `egress` and `obs` are small publication units, without a required module
or process split. authd and proxyd have useful independent service contracts.
Core-only functions qualify for consolidation under 7/26.
Adapters keep independent platform connections, secrets and failure handling.
The matrix's vited, timed and proxy-replacement recommendations do not override
7/26's explicit decisions. proxyd stays.

## Open questions

1. **Approve the minimal bootstrap declaration?** `ant/arizuko.yml:1` declares
   only `core`; `compose/compose.go:936` emits core services directly.
   `installed_packages` cannot select routd before its tables exist.
   Recommendation: `required: true` on required release packages, plus the
   `service` fields above. The release catalog supplies the initial required
   set through the existing installer. Persisted selections govern later boots;
   only release-declared required packages remain mandatory.
   Removal refuses a required package or a selected dependent.
   Optional packages use normal selection and Compose dependencies. This replaces core-name tables with one declaration.
2. **Can upstream services use the same socket?** davd runs dufs through Compose
   (`compose/compose.go:1386`), not an arizuko mux. The litellm package also
   starts a PostgreSQL helper (`template/services/litellm/litellm.yml:60`).
   Recommendation: keep it an ordinary package and use upstream socket support
   only after verifying it. Otherwise retain its declared TCP backend as an
   explicit exception, without a wrapper daemon or fake OpenAPI document.
   Arizuko daemons and adapters use the socket contract. Requiring every upstream
   binary to implement resreg would prevent reuse of existing software.
   Compose-only helpers need no arizuko identity, store key or discovery entry;
   the package lifecycle still owns their deployment artifacts.
3. **Which remote operations can enter a turn?** `resreg/openapi.go:424` emits
   only `x-mcp-when`; `resreg/resreg.go:413` separately derives tool names and
   arguments. A remote schema cannot supply a Go target-resolver closure.
   Recommendation: emit the existing derived MCP descriptor through OpenAPI,
   with an explicit target argument for supported folder/JID operations.
   routd resolves that target and runs `AuthorizeTurn` before forwarding through
   its service identity. The owner daemon still verifies that service caller.
   Missing or unsupported target bindings fail closed. Arbitrary resolver code
   remains outside the package contract until a real resource requires it.
   This needs an authorization review; socket discovery alone cannot settle it.

## Acceptance

1. An outside binary/repository package installs through `package add` and runs
   through Compose. An adapter and a resource service join without core Go map,
   manifest or image edits.
2. Core services pass the same lifecycle, enrollment, discovery and auth checks.
   A fresh instance boots without a package-selection/database cycle.
3. Every declared row applies or causes visible refusal. Failed updates remain
   retryable. Removal touches only owned artifacts and credentials. Group-only
   selection cannot start a service or acquire instance credentials.
4. Keys reach only approved prefixes; core reach matches the call-site test.
   Unknown services receive neither shared `.env` nor wildcard store policy.
   Forged identities and cross-folder targets fail.
5. Discovery recovers from router/producer restarts, stale sockets, health failure
   and removal. Duplicate claims fail; one producer cannot replace another socket.
6. Discovered REST/MCP operations match mounted handlers and preserve target gates.
   Dashboard health agrees. No registration writer, static tile list or parallel
   capability declaration remains.
7. A real guest receives selected skills and tools with existing precedence and
   writable local skills. It cannot access daemon sockets or another group.
   [8/60-minimum-arizuko](../8/60-minimum-arizuko.md) still holds; its package `run`
   command means the Compose command defined here.
