# container

Group setup, skill seeding, and the egress proxy engine.

## Purpose

Owns the files a turn reads and the rules its egress proxy runs under.
`SetupGroup` creates a group folder and seeds its skills; `SeedOutputStyles`,
`SeedMigrateSkill` and `SeedCodexMountTargets` refresh what the platform owns
before each spawn; `EgressRules` and `WaitHealthy` are what a substrate calls
to start and confirm one spawn's proxy.

The turn envelope itself — the mount table, the guest environment, the stdin
frame — is rendered in `runed` and delivered by a substrate (spec 6/41
§"The renderer"). Nothing here knows how a guest is started; the substrate is
the only thing that runs a proxy process (spec 6/41 §"Egress by name, sealed
below the kernel").

## Public API

- `SetupGroup(cfg, folder, seedDir) error` — create a group dir, optionally
  copying `seedDir` (a product template) in as its starting files
- `ComposeGroup(cfg, folder, mix, prior) ([]Blended, error)` — the same group
  dir, seeded from an ordered product MIX instead of one tree (spec 5/28)
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
- `SeedOutputStyles(cfg, claudeDir)`, `SeedMigrateSkill(cfg, claudeDir)` —
  the platform-owned files refreshed on every spawn
- `PackageSkillsDir(cfg) string`, `SeedPackageSkills(cfg, folder) (int, error)`
  — layer the instance's package skills into one group
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

- `runner.go` — group setup and skill seeding: `SetupGroup`/`ComposeGroup` over one `setupGroup` skeleton, the platform-owned refreshes, the copy helpers
- `blend.go` — spec 5/28's composition: the blend table as code. `classify` is its left column; seed-once vs managed is per payload KIND, and a collision refuses before any byte is written
- `runtime.go` — codex mount-target seeding, staging the release `ant/` onto the data dir
- `episodes.go` — recent-episode XML for prompt context
- `egress.go` — the allowlist-to-rules translation and the proxy health wait every substrate calls; starts nothing itself (spec 6/41)

## Related docs

- `ARCHITECTURE.md` (Guest Lifecycle, Mount Security)
