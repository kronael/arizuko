# container

Group setup, package assets, and the egress proxy engine.

## Purpose

Owns the files a turn reads and the rules its egress proxy runs under.
`SetupGroup` creates a group folder. `package sync` writes the `group/` files of
the winning package into it. Package inventory supplies selected assets to
runed; `SeedCodexMountTargets` prepares credential
targets before each spawn. `EgressRules` and `WaitHealthy` are what a substrate calls
to start and confirm one spawn's proxy.

The turn envelope itself — the mount table, the guest environment, the stdin
frame — is rendered in `runed` and delivered by a substrate. Nothing here
knows how a guest is started; the substrate is the only thing that runs a
proxy process.

## Public API

- `SetupGroup(cfg, folder) error` — create a group dir and its web slots; it
  copies no package file.
- `ComposeGroup(cfg, folder, mix, managed) ([]Blended, []string, error)` — the
  same group dir, written from the ordered package selection; the strings are
  the override lines
- `BlendProducts(groupDir, mix, managed) ([]Blended, []string, error)` — the
  group payload write; `Product` names a package, `Blended` reports the files
  one package owns in the group folder, `managed` lists the files that the
  previous write copied
- `UnmanagedPaths(groupDir, mix, managed) ([]string, error)` — the existing
  group files that the mix would write and no earlier write copied; `-f`
  replaces them
- `SanitizeFolder(folder) string` — safe guest, tap and share names
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
- `InstallPackage(dataDir, source, build) (ProductManifest, []string, error)`
  replaces the package payload and returns the manifest and skill names. When
  `build` is true, it runs the optional install command as the invoking user,
  with `PREFIX` set to the final package directory. `package add` and
  `package sync` write the selection row.
- `ReadPackageInventory(dataDir, selected)` reads the packages named by the
  `installed_packages` rows. Core comes first; the other packages follow in the
  order the operator added them. A later package replaces an earlier package on
  the same skill, hook, tool or `group/` path. Core must be present.
- `WriteGatewayCaps(groupDir, cfg)` — the per-turn `.gateway-caps` file
- `RunUID() (int, bool)` — the uid the agent runs as inside the guest, and
  whether the substrate has to be told it
- `EgressRules(allowlist, gatewayURL) (names []string, allowAll bool)` — the
  one translation from a resolved allowlist to the proxy's rules; every
  substrate that starts a proxy reads it
- `WaitHealthy(url, deadline) error` — poll a proxy's `/health` until it
  answers, so a substrate never starts the agent before its proxy can

## The group payload rule

The `group/` directory of a package holds the files that the package puts in
the group folder. Each `package sync` writes every `group/` file of the winning
package into the group folder, and the write replaces any agent edit. The
container stores the list of files that it copied, not their hashes. A file
that a package stops shipping is removed. A file that no package ships stays
the file of the group.

runed adds a deny rule `Edit(~/<path>)` to the Claude Code settings of the turn
for each package file. The rule tells the agent not to edit the file. It is
advisory and is not a security boundary. A file that existed before any package
installed it is refused unless the operator passes `-f`.

## The turn envelope lives in runed

What a guest receives — the mount table, its process environment, the stdin
frame — is rendered once in `runed` (`runed/render.go`) and delivered by a
substrate. `runed/README.md` §"The turn envelope" holds both tables.

## Dependencies

- `core`, `groupfolder`, `audit`, `router`, `chanlib`, `chanlib/chanstore`, `store`, `objstore`, `objstore/dynstore`, `auth`, `obs`, `theme`, `egress/match` (measured with `go list -deps`; `tests/components` pins that `ipc` is not among them)

## Files

- `runner.go` — group setup: `SetupGroup`/`ComposeGroup` over one `setupGroup` skeleton, home initialization and copy helpers
- `blend.go` — the group payload write: package `group/` files and the list of copied files
- `packages.go` — the common installer and selected asset inventory; bundled
  `ant/arizuko.yml` declares core through the same source format
- `runtime.go` — codex mount-target seeding
- `episodes.go` — recent-episode XML for prompt context
- `egress.go` — the allowlist-to-rules translation and the proxy health wait every substrate calls; starts nothing itself

## Related docs

- `runed/README.md` § The turn envelope and § Mounts
