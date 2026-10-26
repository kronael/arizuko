# container

Group setup, package assets, and the egress proxy engine.

## Purpose

Owns the files a turn reads and the rules its egress proxy runs under.
`SetupGroup` creates a group folder and seeds its skills. Package inventory and
target checks support immutable mounts; `SeedCodexMountTargets` prepares credential
targets before each spawn. `EgressRules` and `WaitHealthy` are what a substrate calls
to start and confirm one spawn's proxy.

The turn envelope itself — the mount table, the guest environment, the stdin
frame — is rendered in `runed` and delivered by a substrate. Nothing here
knows how a guest is started; the substrate is the only thing that runs a
proxy process.

## Public API

- `SetupGroup(cfg, database, folder, seedDir) error` — initialize required core
  and create a group dir, optionally copying `seedDir` as its starting files.
  The store is borrowed; only pre-store instance creation passes nil.
- `ComposeGroup(cfg, database, folder, mix, prior) ([]Blended, error)` — the same group
  dir, seeded from an ordered product MIX instead of one tree
- `BlendProducts(groupDir, mix, prior) ([]Blended, error)` — the blend itself,
  per payload kind; `Product` names a mix entry, `Blended` reports what one
  product owns, what changed, and what was left alone as locally edited
- `SanitizeFolder(folder) string` — safe guest, tap and share names
- `MigrationVersion(path) int` — reads `MIGRATION_VERSION` file
- `ReadRecentEpisodes(groupDir) string` — XML snippet for prompt
- `WriteTasksSnapshot`, `WriteGroupsSnapshot` — per-group snapshots
- `CopySession(groupDir, srcUUID, dstUUID) (bool, error)` — fork a session:
  copy `<groupDir>/.claude/projects/-home-node/<src>.jsonl` to `<dst>.jsonl`
  and restamp its `sessionId`. The slug `-home-node` is fixed because every
  guest mounts the group dir at `$HOME=/home/node`, so Claude Code slugs the
  same path for every folder; deriving it per folder would read identity off
  a path. The write goes to a temp file, then a rename. A missing parent file
  is a WARN and answers false, and routd then clears the child's observed
  cursor (`ROUTING.md` § Topic Routing)
- `SeedCodexMountTargets(codexDir)` — pre-create the files codex overmounts
- `EnsureCorePackage(cfg, database) error` — select the configured release's
  required system core under the caller's package lock. A borrowed store completes
  its receipt; nil selects filesystem state before the store exists. Matching
  optional receipts retain their fields. Group files retain their bytes.
- `ReadPackageInventory(dataDir, folder)` reads active system and exact-folder
  selections under the caller's lock. `CheckPackageMountTargets` rejects writable
  managed payloads; `PreparePackageMountTargets` creates empty targets after full
  preflight. `SetPackageSkillDisabled` preserves existing marker bytes.
- `WriteGatewayCaps(groupDir, cfg)` — the per-turn `.gateway-caps` file
- `MaterializeAppSrc(cfg) string` — stage the release's `ant/` on the data dir
- `RunUID() (int, bool)` — the uid the agent runs as inside the guest, and
  whether the substrate has to be told it
- `EgressRules(allowlist, gatewayURL) (names []string, allowAll bool)` — the
  one translation from a resolved allowlist to the proxy's rules; every
  substrate that starts a proxy reads it
- `WaitHealthy(url, deadline) error` — poll a proxy's `/health` until it
  answers, so a substrate never starts the agent before its proxy can

## The turn envelope lives in runed

What a guest receives — the mount table, its process environment, the stdin
frame — is rendered once in `runed` (`runed/render.go`) and delivered by a
substrate. `runed/README.md` §"The turn envelope" holds both tables.

## Dependencies

- `core`, `groupfolder`, `audit`, `router`, `chanlib`, `chanlib/chanstore`, `store`, `objstore`, `objstore/dynstore`, `auth`, `obs`, `theme`, `egress/match` (measured with `go list -deps`; `tests/components` pins that `ipc` is not among them)

## Files

- `runner.go` — group setup: `SetupGroup`/`ComposeGroup` over one `setupGroup` skeleton, home initialization and copy helpers
- `blend.go` — the composition of a product mix: the blend table as code. `classify` is its left column; seed-once vs managed is per payload KIND, and a collision refuses before any byte is written
- `package_lifecycle.go` — package activation and receipt persistence through a borrowed store
- `core_package.go` — required core initialization and canonical package collision checks;
  `ant/arizuko.toml`, `CLAUDE.md` and `skills/` define core's fixed payload
- `runtime.go` — codex mount-target seeding
- `episodes.go` — recent-episode XML for prompt context
- `egress.go` — the allowlist-to-rules translation and the proxy health wait every substrate calls; starts nothing itself

## Related docs

- `runed/README.md` § The turn envelope and § Mounts
