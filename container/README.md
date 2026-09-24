# container

Group setup, skill seeding, and the egress proxy engine.

## Purpose

Owns the files a turn reads and the proxy it reaches the network through.
`SetupGroup` creates a group folder and seeds its skills; `SeedOutputStyles`,
`SeedMigrateSkill` and `SeedCodexMountTargets` refresh what the platform owns
before each spawn; `StartEgressProxy` starts one allowlisted proxy per spawn.

The turn envelope itself — the mount table, the guest environment, the stdin
frame — is rendered in `runed` and delivered by a substrate (spec 6/41
§"The renderer"). Nothing here knows how a guest is started.

## Public API

- `SetupGroup(cfg, folder, seedDir) error` — create a group dir, optionally
  copying `seedDir` (a product template) in as its starting files
- `ComposeGroup(cfg, folder, mix, prior) ([]Blended, error)` — the same group
  dir, seeded from an ordered product MIX instead of one tree (spec 5/28)
- `BlendProducts(groupDir, mix, prior) ([]Blended, error)` — the blend itself,
  per payload kind; `Product` names a mix entry, `Blended` reports what one
  product owns, what changed, and what was left alone as locally edited
- `EnsureRunning() error` — verify docker daemon
- `CleanupOrphans(instance, image)` — stop stale `arizuko-*`
- `SanitizeFolder(folder) string` — safe docker network/container names
- `StopContainerArgs(name) []string` — docker stop args
- `MigrationVersion(path) int` — reads `MIGRATION_VERSION` file
- `ReadRecentEpisodes(groupDir) string` — XML snippet for prompt
- `WriteTasksSnapshot`, `WriteGroupsSnapshot` — per-group snapshots
- `CopySession(groupDir, srcUUID, dstUUID) error` — fork sessions
- `SeedCodexMountTargets(codexDir)` — pre-create the files codex overmounts
- `SeedOutputStyles(cfg, claudeDir)`, `SeedMigrateSkill(cfg, claudeDir)` —
  the platform-owned files refreshed on every spawn
- `SeedPackageSkills(cfg, folder) (int, error)` — layer the instance's package
  skills into one group
- `WriteGatewayCaps(groupDir, cfg)` — the per-turn `.gateway-caps` file
- `MaterializeAppSrc(cfg) string` — stage the release's `ant/` on the data dir
- `RunUID() (int, bool)` — the uid the agent runs as inside the guest, and
  whether the substrate has to be told it
- `StartEgressProxy(cfg, id, spawnName) (EgressSpawn, error)` — this spawn's
  own proxy, healthy before it returns; `EgressSpawn.Stop()` tears it down
- `SweepEgressProxies(prefix)` — reap the proxies a dead runed left behind
- `FolderNetwork(prefix, folder) string` — per-folder network name
- `PickIP(subnet) string` — random IP in /24 for egress isolation
- `EgressConfig` — this instance's egress isolation inputs
- `Profile` — the Docker substrate's declaration (`substrate.Profile`);
  `EgressConfig.Effective()` is the one verdict on whether egress isolation
  runs, with the reason when it does not

## The turn envelope lives in runed

What a guest receives — the mount table, its process environment, the stdin
frame — is rendered once in `runed` (`runed/render.go`) and delivered by a
substrate. `runed/README.md` §"The turn envelope" holds both tables.

## Dependencies

- `core`, `groupfolder`, `audit`, `router`, `chanlib`, `chanlib/chanstore`, `store`, `objstore`, `objstore/dynstore`, `auth`, `obs`, `theme`, `egress`, `egress/match`, `substrate` (measured with `go list -deps`; `tests/components` pins that `ipc` is not among them)

## Files

- `runner.go` — group setup and skill seeding: `SetupGroup`/`ComposeGroup` over one `setupGroup` skeleton, the platform-owned refreshes, the copy helpers
- `blend.go` — spec 5/28's composition: the blend table as code. `classify` is its left column; seed-once vs managed is per payload KIND, and a collision refuses before any byte is written
- `runtime.go` — docker daemon checks, container cleanup, codex seed
- `episodes.go` — recent-episode XML for prompt context
- `network.go` — per-folder Docker network creation for egress isolation
- `egress.go` — the per-spawn egress proxy, IP allocation, the substrate declaration and `Effective`

## Related docs

- `ARCHITECTURE.md` (Container Lifecycle, Mount Security)
