# Extending arizuko

Catalog of extension points. Keep current as the system evolves.

Extension points are how you **add an integration** — a new channel
adapter, a TTS backend, an oracle skill, a per-folder mount, a custom
autocall, a scheduled task, a network-egress rule. They are NOT how you
change the **system core** (gateway, store, ipc, auth, grants, proxyd,
webd, dashd, timed, onbod, container runner, chanlib/chanreg). Core
evolves as a unit through specs, not via these extension points. See
[README.md](README.md) for the core-vs-integration breakdown and
[ARCHITECTURE.md](ARCHITECTURE.md) for the package graph.

## Extension points

| Point         | Location               | Extensible by  | Mechanism                                   |
| ------------- | ---------------------- | -------------- | ------------------------------------------- |
| Channels      | external containers    | Developer      | HTTP protocol                               |
| Slink         | `webd/slink*.go`       | External agent | Chat UI + MCP transport at `/slink/<token>` |
| Actions       | MCP tools              | Agent/Plugin   | Registry + MCP                              |
| Autocalls     | `gateway/autocalls.go` | Gateway dev    | Registry slice                              |
| Routing rules | `router/`              | Agent          | MCP tools                                   |
| Mounts        | `container/`           | Agent          | Container config                            |
| Skills        | `ant/skills/`          | Agent          | File-based                                  |
| Tasks         | `timed/`               | Agent          | IPC actions                                 |
| Diary         | `diary/`               | Agent          | File-based                                  |
| Network rules | `store/network.go`     | Operator       | CLI + DB rows                               |

## Adding an autocall

Autocalls inject zero-arg, one-line, pure-read facts into the
`<autocalls>` block at the top of every prompt. Cheaper than an MCP
tool when the schema cost exceeds the data returned: no agent-visible
schema, no tool call, one line of output per turn.

Rules:

- Result is ≤ 1 line of text. Empty string = skip the line.
- No args, no I/O, no locks. Must resolve in microseconds.
- Derives from `AutocallCtx` (instance, folder, session, tier, now).
- If any of these don't hold, use an MCP tool instead.

Add an entry to the registry slice in `gateway/autocalls.go`:

```go
{"world", func(c AutocallCtx) string {
    return strings.SplitN(c.Folder, "/", 2)[0]
}},
```

Then update `ant/skills/self/SKILL.md` autocalls section and ship a
migration under `ant/skills/self/migrations/`.

## Designing MCP tools for LLMs

The MCP description is the model's training material every turn. It is
read on every prompt, costs tokens, and shapes which tool the model
picks. Two principles follow.

**Descriptions answer "when", not just "what".** Every tool description
states _use when X_ and _not for Y_, naming the sibling tool that
covers Y. The model picks instantly instead of reasoning about
disambiguation at call time. See `ipc/ipc.go` registrations
(`reply`, `like`, `reset_session`, `register_group`, …) for the
canonical pattern.

**No surrogates — `Unsupported(...)` with a hint.** When a verb has no
native primitive on a platform, the adapter returns
`chanlib.Unsupported(tool, platform, hint)` carrying a concrete
alternative tool the agent should call instead. Do not synthesize a
fake implementation by gluing other primitives together. The hint
travels through HTTP 501 → `*chanlib.UnsupportedError` →
`toolMaybeUnsupported` and is rendered to the agent as
`unsupported: <tool> on <platform>\nhint: <alternative>`.

**Distinct intents → distinct tool names.** Default to one tool per
intent. A sharp per-intent description outperforms a fuzzy umbrella
description with a `kind`/`mode`/`type` enum: the umbrella forces the
model to disambiguate at call time and dilutes signal in every other
tool's description by proximity.

Only collapse two names into one tool when the action is mechanically
identical AND the same description naturally covers both — e.g.
`send_reply` covers comment/reply because both create a threaded
response to a parent message. Do NOT collapse repost/forward/quote
into `share(kind=…)`; three intents, three tools.

Architectural overlap under the hood is fine. Email's `forward` may
compile to `send` + `Fwd:` subject; Telegram's `forward` uses a native
protocol field. Both expose `forward` as a distinct MCP tool because
the agent's intent is the same ("show X this thing I saw"). The
adapter does the translation.

UNIX precedent: `cp` / `mv` / `ln` are three commands with three man
pages, not `relocate(kind=copy|move|link)`.

The autocall-vs-MCP-tool decision (above) is the same principle on a
different axis: minimize the model's per-turn cost of choosing and
calling. Zero-arg pure-read facts → autocall. Distinct intents →
distinct tools.

## Verb support matrix

The 12 outbound MCP verbs and their per-platform native support. An
empty cell means the adapter returns `*UnsupportedError` with a
concrete hint.

| Verb         | discd | mastd | bskyd | reditd | teled | emaid | linkd | whapd | twitd |
| ------------ | ----- | ----- | ----- | ------ | ----- | ----- | ----- | ----- | ----- |
| `send`       | ✓     | ✓     | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     |
| `reply`      | ✓     | ✓     | ✓     | ✓      | ✓     | ✓     | ✓     | ✓     | ✓     |
| `send_file`  | ✓     |       |       |        | ✓     |       |       | ✓     | ✓     |
| `send_voice` | ✓     |       |       |        | ✓     |       |       | ✓     |       |
| `post`       | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |
| `like`       | ✓     | ✓     | ✓     | ✓      | ✓     |       |       | ✓     | ✓     |
| `delete`     | ✓     | ✓     | ✓     | ✓      |       |       |       |       | ✓     |
| `forward`    |       |       |       |        | ✓     |       |       | ✓     |       |
| `quote`      |       |       | ✓     |        |       |       |       |       | ✓     |
| `repost`     |       | ✓     | ✓     |        |       |       |       |       | ✓     |
| `dislike`    |       |       |       | ✓      |       |       |       |       |       |
| `edit`       | ✓     | ✓     |       |        | ✓     |       |       | ✓     |       |

## Inspect tools

Read-only MCP introspection family, registered in `ipc/inspect.go`:
`inspect_messages`, `inspect_routing`, `inspect_tasks`,
`inspect_session`. Delegate to `store.*` accessors; no destructive
operations (those stay in `control_*`). Tier 0 sees all instances; tier
≥1 is scoped to its own folder subtree. Extend by adding a handler to
`registerInspect` and wiring a fn into `ipc.StoreFns`.

## Adapter `/health` contract

Each adapter exposes `GET /health` returning `{ok, name, status, caps}`.
`chanlib.NewAdapterMux` provides the canonical implementation; see
`chanlib/README.md`.

## Skills

Three scopes, no inheritance:

- `ant/skills/` — global, baked into image, read-only
- `groups/<folder>/.claude/skills/` — per-group, persistent
- `.claude/skills/` — per-session, seeded from global on first spawn

Canonical definitions at `/workspace/self/ant/skills/` (ro mount) for
`/migrate` diffing. `MIGRATION_VERSION` integer + `/migrate` skill
drive upgrades.

Skill layout:

```
<name>/
  SKILL.md              # required: prompt injection
  CLAUDE.md             # optional
  migrations/           # optional numbered upgrade scripts
```

## Host-tool capabilities

Some integrations are **pure host-tool surfaces** — no daemon, no MCP,
no protocol. The operator installs a CLI in the agent image (or
mounts host state into it) and ships a skill that drives it as a
subprocess. The agent sees an ordinary command on `PATH`; the
skill is the discovery surface; auth flows from a host-side mount or
folder secret. Distinct from MCP tools (in-band, schema-typed,
gateway-mediated) and channel adapters (out-of-band, HTTP).

Currently shipping:

| Capability | Binary  | Skill                        | Auth                                                                                    |
| ---------- | ------- | ---------------------------- | --------------------------------------------------------------------------------------- |
| `oracle`   | `codex` | `ant/skills/oracle/SKILL.md` | `HOST_CODEX_DIR` mount on gated **OR** `CODEX_API_KEY` / `OPENAI_API_KEY` folder secret |

Adding one:

1. Install the binary in `ant/Dockerfile` (pinned version where
   upstream supports it).
2. If the tool needs host-side credentials/state, add a `HOST_*_DIR`
   env on `core.Config`, plumb through `compose.Generate` and
   `container.runner` so it's bind-mounted into spawns. Pattern:
   `HOST_CODEX_DIR` (`v0.33.9`).
3. Write `ant/skills/<name>/SKILL.md` with sharp frontmatter
   `description` (this is what `/dispatch` matches on), a "when to
   invoke" section, copy-pasteable invocations, and a missing-auth
   fallback that fails soft instead of crashing the turn.
4. Bump `ant/skills/self/MIGRATION_VERSION` so the auto-migrate
   broadcast fires on next spawn.

Skill body shape: see `ant/skills/oracle/SKILL.md` as the reference.

## Permission tiers

Folder-depth model. Tier = `min(folder.split("/").length, 3)`, except
`root` = 0. Path is identity; depth picks the tier slot.

| Tier | Depth | Example             |
| ---- | ----- | ------------------- |
| 0    | 0     | `root`              |
| 1    | 1     | `atlas`             |
| 2    | 2     | `atlas/support`     |
| 3+   | 3+    | `atlas/support/web` |

Suggested human labels per depth (`world / org / branch / unit /
thread`) live in `ant/CLAUDE.md` — advisory only, the system reads
paths, not labels. No inheritance, no escalation, no custom tiers.
`escalate_group` sends a message to the parent; it does not grant
permissions. See `specs/4/11-auth.md` and `specs/4/19-action-grants.md`.
