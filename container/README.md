# container

Docker runner, mount setup, skill seeding for the agent container.

## Purpose

Spawns one `docker run -i --rm` per agent invocation. Builds mounts,
seeds `settings.json` and `.claude/skills/`, pipes input JSON on stdin,
waits for exit. Per-turn results return over MCP via `submit_turn`;
stdout is discarded. Also owns `SetupGroup` to create new group folders.

## Public API

- `Run(cfg, folders, in) Output` — single container run
- `Input`, `Output`, `Runner`, `DockerRunner`
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
- `SeedCodexDirs(groupsDir)` — pre-create `.codex/` per group
- `FolderNetwork(prefix, folder) string` — per-folder network name
- `PickIP(subnet) string` — random IP in /24 for egress isolation
- `EgressConfig` — crackbox isolation settings

## Env injection

Container env carries model credentials only: the operator anchors from the
host `.env` (`store.EnvProfileKeys` — `ANTHROPIC_API_KEY`,
`CLAUDE_CODE_OAUTH_TOKEN`, `OPENAI_API_KEY`, `CODEX_API_KEY`, required for LLM
calls), overlaid with the triggering user's own rows for those same keys
(BYOA, `routd.DB.EnvProfileSecrets`). Capability credentials — folder- or
user-scoped — are broker-resolved on the host at tool-call time and never
enter the container (specs `5/13`, `5/14`). Agent model and query timeout
are set via `ARIZUKO_MODEL` and `ARIZUKO_QUERY_TIMEOUT_MS` env vars
(read by ant's index.ts and claude.ts at module load).

## Mounts

Every container:

- `groupDir` → `/home/node` (rw)
- `HOST_APP_DIR` → `/opt/arizuko` (ro)
- `ipc/` → `/run/ipc` (rw, for MCP socket)
- `groups/<world>/share/` → `/var/lib/share` (rw; ro if `share_mount` grant with `readonly=true`)

Elevated `/root` turn only:

- `GROUPS_DIR` → `/var/lib/groups` (rw)

With the `web:publish` grant:

- `WEB_DIR/pub/` → `/var/lib/www` (ro, whole tree)
- `WEB_DIR/pub/<folder>/` → `~/public_html` (rw)
- `WEB_DIR/priv/<folder>/` → `~/private_html` (rw)

When `HOST_CODEX_DIR` set:

- `groupDir/.codex/` → `~/.codex` (rw, per-group)
- `HOST_CODEX_DIR/auth.json` → `~/.codex/auth.json` (ro, overmount)
- `HOST_CODEX_DIR/config.toml` → `~/.codex/config.toml` (ro, overmount)

Additional mounts from `group.toml` validated via `mountsec`.

## Dependencies

- `core`, `groupfolder`, `mountsec`, `ipc`, `audit`, `diary`, `grants`, `router`, `chanlib`, `crackbox/pkg/client`

## Files

- `runner.go` — docker invocation, lifecycle, idle/hard-deadline timers, mount assembly, settings seeding; `SetupGroup`/`ComposeGroup` over one `setupGroup` skeleton
- `blend.go` — spec 5/28's composition: the blend table as code. `classify` is its left column; seed-once vs managed is per payload KIND, and a collision refuses before any byte is written
- `runtime.go` — docker daemon checks, container cleanup, codex seed
- `episodes.go` — recent-episode XML for prompt context
- `network.go` — per-folder Docker network creation for egress isolation
- `egress.go` — crackbox registration/unregistration, IP allocation

## Related docs

- `ARCHITECTURE.md` (Container Lifecycle, Mount Security)
