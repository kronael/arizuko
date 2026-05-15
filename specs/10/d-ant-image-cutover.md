---
status: unshipped
---

# Ant image cutover — replace TS with Go binary

Phase 2 of the ant Go runtime. The Go binary lives in
`arizuko/ant/cmd/ant/`; `ant/Dockerfile` builds the image directly
from the `ant/` subtree. `ant:latest`'s ENTRYPOINT switches from
`node /app/src/index.js` to `/usr/local/bin/ant`. Nothing above the
image — `container.Runner`, `gated`, gateway — changes.

Sibling to [c-ant-mcp-runtime.md](c-ant-mcp-runtime.md), which
defines the runtime. This spec is the operational cutover.

## Why a separate spec

Two different risks:

1. The runtime (designed in `c-`) can be wrong (wire shape, race,
   end-of-turn semantics). Discovered via real-claude smoke tests in
   the `claude-serve` incubator.
2. The cutover can be wrong (image size, layer cache, base CVE
   exposure, `claude` pinning, env wiring, `~/.claude` mount layout).
   Discovered by running the new image against real arizuko groups.

Decoupling them means a runtime bug doesn't block cutover planning,
and a cutover concern doesn't pollute the runtime design.

## Where the code moves

The Go packages currently incubating in `claude-serve/` move into the
arizuko tree:

```
claude-serve/cmd/ant/           → arizuko/ant/cmd/ant/
claude-serve/internal/claude/   → arizuko/ant/pkg/runtime/claude/
claude-serve/internal/session/  → arizuko/ant/pkg/runtime/session/
claude-serve/internal/mcp/      → arizuko/ant/pkg/mcp/
claude-serve/test/smoke/        → arizuko/ant/test/smoke/
```

Import-path rewrite from `github.com/onvos/claude-serve/...` to
`<arizuko-module>/ant/...`. After the move, `claude-serve/` is
archived.

The pre-existing folder loader at `ant/pkg/agent/` is unaffected (it
was already designed alongside `b-`); the runtime packages slot in
next to it.

## Dockerfile layout

Lives at `ant/Dockerfile`. Build context is `ant/` — nothing in
the file reaches up into the rest of the arizuko tree. The image
could be built from a copy of just the `ant/` subtree (same
orthogonality test as the Go imports).

```Dockerfile
# stage 1: build the binary
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-s -w" -trimpath \
    -o /out/ant ./cmd/ant

# stage 2: runtime
FROM debian:stable-slim
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -m -u 1000 -s /bin/bash agent

# pinned claude CLI — version bumped via single line, here
ARG CLAUDE_VERSION=2.1.119
RUN curl -fsSL -o /usr/local/bin/claude \
    https://github.com/anthropics/claude-code/releases/download/v${CLAUDE_VERSION}/claude-linux-amd64 \
 && chmod +x /usr/local/bin/claude

COPY --from=build /out/ant /usr/local/bin/ant
COPY skills /opt/ant/skills

ENV ANT_CLAUDE_BIN=/usr/local/bin/claude \
    ANT_SKILLS_DIR=/opt/ant/skills \
    CLAUDE_PROJECTS_DIR=/home/agent/.claude/projects \
    CLAUDE_CODE_ENTRYPOINT=ant

USER agent
WORKDIR /workspace
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/ant", \
            "/workspace", "--mcp", "--socket=/run/ipc/ant.sock"]
```

Notes:

- `tini` is PID 1 — proper signal forwarding to the Go binary and
  the `claude` grandchild.
- `--from=anthropics/claude:...` (if Anthropic publishes a base
  image) is the cleaner alternative to the `curl` download; pick
  whichever is the actual distribution channel. The pinned version is
  expressed as one ARG line either way.
- `skills/` baked into `/opt/ant/skills` is the curated portable set
  from `b-ant-standalone.md`. The agent's per-folder skills under
  `/workspace/.claude/skills/` layer on top.

## Env-var hookups the Go binary needs

Two are already wired (`cmd/ant/main.go` accepts `--socket`); two
need adding before the cutover:

- `ANT_CLAUDE_BIN` → if set, used as `Config.Binary` in
  `internal/claude/`. Default: `claude` on PATH (unchanged for
  standalone use).
- `CLAUDE_PROJECTS_DIR` → forwarded as env var to the spawned
  `claude` subprocess. Allows the bind-mount layout below.

Both are reads from `os.Getenv`, ~10 lines of Go. No protocol changes.

## Runtime mount layout (what `gated` provides)

`gated`'s `DockerRunner.Run` already constructs the container's
mounts. After cutover the mount set is the same, with one addition:

```
/workspace                 ← bind-mount the group folder       (rw)
/run/ipc/ant.sock          ← MCP socket the new ant serves     (rw, new)
/run/ipc/gated.sock        ← bind-mount of gated's IPC socket  (rw, existing)
/home/agent/.claude        ← tmpfs OR bind for session storage (rw, opt)
```

The `gated.sock` mount is unchanged — that's arizuko's MCP-server
exposed _to_ the agent (the model uses it for diary, recall, etc.).
The new `ant.sock` is the inverse — the MCP-server ant exposes _to_
`gated` for chat IO. `container.Runner` already creates both paths;
no code change required there.

`/home/agent/.claude` defaults to tmpfs (ephemeral sessions). The
portability spec (`../7/7-ant-portability.md`) wires it to a per-folder bind when
`--include-sessions` is requested at export time.

## Container.Runner contract — what stays the same

`container/runner.go:DockerRunner.Run` keeps:

- `Input` shape (`Prompt`, `SessionID`, `ChatJID`, `Folder`,
  `AsstName`, `Secrets`, `Soul`, `SystemMd`, ...) — unchanged.
- `Output` shape (`Status`, `Result`, `NewSessionID`, `Error`).
- Stdin: serialized `ContainerInput` JSON, one line.
- Stderr: `[ant]` markers reset the idle timer.
- Hard deadline + soft deadline (SIGUSR1 2 min before) + idle timeout.

The new Go binary's main loop receives the `ContainerInput` line on
stdin, drives one or more `claude` sessions via MCP-internal calls,
emits `[ant]` markers on stderr at the same points the TS runtime
emitted them, writes `ContainerOutput` JSON on stdout, exits with
the same exit codes the TS runtime used.

Acceptance test: a recorded fixture suite of (Input JSON →
expected `[ant]` marker sequence + Output JSON) under
`ant/test/contract/` runs against both the TS image (currently
`ant:latest`, soon `ant:ts-fallback`) and the Go image and produces
byte-identical Output for every fixture. Drift in marker count or
shape blocks the cutover.

## Soak protocol

1. **Build** `ant:next` from the new Dockerfile in CI; push to the
   registry.
2. **Canary**: set `AGENT_IMAGE=ant:next` for **one** specific
   group on a non-production instance. Run for 24h. Diff diary
   writes, channel messages, container exit codes against the same
   group's TS run from the prior 24h.
3. **Promote**: if clean, retag `ant:latest` → `ant:ts-fallback`,
   then `ant:next` → `ant:latest`. Bump arizuko version.
4. **Watch**: existing groups pick up `ant:latest` on next spawn.
   For 7d, monitor: container exit codes, p50/p99 turn latency,
   stderr volume, OOM signals. Compare against the prior week.
5. **Retire**: after 7d clean, delete the TS source tree
   (`ant/src/`), the TS Dockerfile, and the `ant:ts-fallback` tag.
   This step is the cutover's commit point.

## Rollback

At any step before retirement: flip `AGENT_IMAGE=ant:ts-fallback`.
The TS image stays as-is; everything above it (gated, gateway, the
container contract) is unchanged, so the swap is one env var.

## Migration entry

Migration `0XX-ant-go-image-cutover.md` is the single source of
truth for the version this lands in. It records: the
`CLAUDE_VERSION` pinned at build time, the `ant:next` SHA, the
canary group ID, the start time of the 7-day soak.

## Image size budget

Target: < 350 MB compressed (the `claude` binary is the floor; the
rest is base + ant + ca-certs + tini).

| Layer                           | Compressed size |
| ------------------------------- | --------------- |
| `debian:stable-slim` base       | ~80 MB          |
| `ca-certificates`, `tini`, user | ~5 MB           |
| `claude` CLI binary             | ~245 MB         |
| `ant` Go binary                 | ~8 MB           |
| `skills/`                       | ~2 MB           |
| **Total**                       | **~340 MB**     |

vs the current TS image: ~600 MB (Node + Bun + node_modules + claude

- ant TS). Roughly half-size, statically linked, fewer CVEs in the
  runtime layer.

## Out of scope

- The runtime design itself — see `c-`.
- Multi-arch builds (linux/arm64 alongside amd64). Add when an
  arm64 deployment target lands; until then, amd64 only matches
  arizuko's current production fleet.
- Distroless / scratch base — debian-slim chosen for `tini` and the
  `claude` binary's potential glibc dependence. Re-evaluate once
  Anthropic publishes static binaries.
- `CLAUDE_PROJECTS_DIR` per-folder binding — described here but
  enabled by the portability spec (`../7/7-ant-portability.md`), not this one.

## Open questions

1. **`claude` binary distribution.** Where does the Dockerfile fetch
   it from? If Anthropic publishes a stable URL (GitHub releases or
   a base image), use that. If not, vendor the binary into the
   arizuko build cache and `COPY` it in. The `ARG CLAUDE_VERSION` is
   the single line that changes either way.
2. **Image registry path.** Today: `<org>/arizuko-ant:latest`. Keep
   the existing path; the Go binary doesn't change naming.
3. **CI cache.** The `apt-get` and `curl claude` layers should be
   buildkit-cached separately from the Go build stage. Validate
   cache hit rates after one round of builds.
4. **Multi-stage `--from=anthropics/claude:X`.** Confirm whether
   Anthropic publishes a versioned base image; if yes, prefer it
   over `curl`-from-releases.

## Acceptance

- `ant/Dockerfile` builds `ant:next` from a clean checkout in CI in
  under 5 min (with cache, under 60s).
- Recorded contract-test fixture suite produces byte-identical
  Output for every fixture against both images.
- One canary group runs on `ant:next` for 24h with zero
  exit-code regressions and within ±10% turn latency.
- 7-day soak across all groups completes without rollback.
- `ant/src/` (TS), the old TS Dockerfile, and `ant:ts-fallback` are
  deleted in one commit at the end of the soak.

## Relation to other specs

- [c-ant-mcp-runtime.md](c-ant-mcp-runtime.md) — the runtime this
  spec packages and deploys.
- [b-ant-standalone.md](b-ant-standalone.md) — the folder layout
  the image consumes via `/workspace`.
- [../7/7-ant-portability.md](../7/7-ant-portability.md) — wires
  `CLAUDE_PROJECTS_DIR` to a per-folder bind when sessions ship
  with the agent.
- [../9/](../9/) — `mcp-fw` / standalone hardening; the MCP socket
  permissions on `/run/ipc/ant.sock` use the same conventions.
