# container

Docker runner, mount setup, skill seeding for the agent container.

## Purpose

Spawns one `docker run -i --rm` per agent invocation. Resolves the group
folder, builds mounts through `mountsec`, seeds `settings.json` and
`.claude/skills/`, pipes input JSON on stdin, then waits for exit.
Per-turn results return over MCP via `submit_turn`; stdout is discarded.
Also owns `SetupGroup` used by `onbod` and gateway to create new group
folders from a prototype.

## Public API

- `Run(cfg *core.Config, folders *groupfolder.Resolver, in Input) Output` — single container run
- `Input`, `Output`, `Runner`, `DockerRunner`
- `SetupGroup(cfg, folder, prototype) error` — seed a new group dir
- `EnsureRunning() error` — verify docker daemon
- `CleanupOrphans(instance, image)` — stop stale `arizuko-*`
- `SanitizeFolder(folder) string`
- `StopContainerArgs(name) []string`
- `MigrationVersion(path) int` — reads `ant/skills/self/MIGRATION_VERSION`
- `ReadRecentEpisodes(groupDir) string`
- `WriteTasksSnapshot`, `WriteGroupsSnapshot` — per-container snapshots

## Dependencies

- `core`, `groupfolder`, `mountsec`, `store` (indirect via Input)

## Files

- `runner.go` — docker invocation, lifecycle, idle/hard-deadline timers
- `runtime.go` — seed settings, skills, `.claude.json`
- `episodes.go` — recent-episode snippets for prompt

## Related docs

- `ARCHITECTURE.md` (Container Lifecycle, Mount Security, Docker-in-Docker Paths)
