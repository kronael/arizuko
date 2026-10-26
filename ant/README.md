# ant

The agent runtime for arizuko. Wraps Claude Code (via
`@anthropic-ai/claude-agent-sdk`), injects group identity + skills +
memory, and exposes MCP tools over routd's unix socket.

## What it does

Every turn runs in a fresh KVM guest booted from this image's exported rootfs.
`ant/src/index.ts` is the entrypoint: it reads stdin for the prompt,
loads the group's `PERSONA.md` / `CLAUDE.md` / `~/.claude/settings.json`,
assembles MCP servers (core `arizuko` server via socat to `gated.sock` +
third-party connectors from `settings.json`), runs the Claude Code SDK,
and reports the outcome via the `submit_turn` JSON-RPC method back to
`routd`.

## TS runtime (`src/`)

The shipping runtime uses `@anthropic-ai/claude-agent-sdk` 0.3.278
(`bun.lock`, measured at 7baffbb0). MCP servers are assembled in
`src/mcp-servers.ts`:

### The harness

ant does not implement an agent loop; it wraps one. `src/claude.ts`
drives Claude Code via `@anthropic-ai/claude-agent-sdk` — spawn the
harness, reduce its message stream to frames (`ClaudeTurn`), report the
turn over `submit_turn`. Only delivery tools send an answer; printed prose
stays in the transcript. A nonempty `nudge` response resumes the same turn ID
and continuity token before completion. Claude and Codex Stop hooks ask the
same `check_turn` decision. Pi is refused until it has delivery tools.
Model calls, tool execution, retries and
session state are all the harness's, never ant's.

A claude run is one session, and the session ends when Claude Code says so,
not at the first `result`. The prompt stream stays open until Claude Code
reports `session_state_changed: idle` after a result. It reports `idle` only
when no background Agent runs and no notification waits, so a
`task-notification` that arrives after the main turn is handled with working
tools. A background shell does not hold the session. ant asks for the state
events with `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS`; a Claude Code that
sends none ends the session at its first result. ant holds each `result`
frame and delivers the last one, so `submit_turn` runs once per run, at the
end of the session. While the session waits on a task, ant moves the steer
spool into the prompt stream as `<user-steering>`, because no tool hook runs
then. A Stop that lists background tasks skips `check_turn`; the reply-owed
check runs on the Stop that lists none.

Harness independence lives at the **process boundary**: the agent tool
surface + `submit_turn` on the unix socket routd serves is a wire
protocol, so a different harness replaces ant as its own process without
touching arizuko. Inside ant, `src/backend.ts` is the harness seam,
stated as arizuko's requirements: `Turn` in, `Frame`s out
(continuity / status / result), one `run()` per harness plus a
`HarnessProfile` declaring its capabilities. Continuity is an opaque
token each harness packs (claude a `sessionId#resumeAt` pair, codex its
thread id); a result with `retryFresh` retries the turn fresh instead of
delivering a resume-miss diagnostic. One file per harness — `claude.ts`
(default), `codex.ts` (`codex exec --json`, MCP via `-c mcp_servers.*`),
`pi.ts` (`pi -p --mode json`, native skills, no MCP client).
`ARIZUKO_BACKEND` selects; unknown values fail the turn loudly. Skills
reach codex/pi through the load-time guard in `src/skills-scan.ts` — the
same skillguard table, a different hook point. Extension recipe in
`EXTENDING.md` § "Swapping the agent harness".

- **`arizuko` core server** — socat bridge to `/run/ipc/gated.sock`,
  `alwaysLoad: true` so `send`/`reply`/`inspect_*`/`send_file` stay
  eager every turn.
- **Third-party connectors** — served over that SAME socket as arizuko
  tools (`ipc.StoreFns.Connectors`: built-in providers plus the
  operator's `connectors.toml`), so a connector call meets the grant
  check, the audit row and `CheckHold` like every other. A server the
  agent declares in `~/.claude/settings.json` is NOT read: that path
  handed tools straight to the harness and the agent writes that file
  itself, so it was a self-grant (BUGS J1, F76).

No arizuko tool is deferred: one server, `alwaysLoad`, so a turn's tool
list holds every arizuko and connector tool the turn may call. Claude Code
defers its own `TaskStop`; the agent loads it with `ToolSearch`.

## The resident file

`ant/CLAUDE.md` is the one file loaded into every turn. The selected core
package supplies it read-only at `~/.claude/CLAUDE.md`. Claude Code reads it as user
memory through `settingSources: ['project', 'user']` with the cwd at the
guest's home (`src/claude.ts:512`); codex and pi receive the same file rendered into
their system prompt. The repo-root `CLAUDE.md` is the maintainers' file and
never reaches a guest.

Codex receives `project_doc_fallback_filenames=["CLAUDE.md"]` for fresh and
resumed turns. Codex checks the group's `/home/node/CLAUDE.md` after
`AGENTS.override.md` and `AGENTS.md` in that directory. User config stays disabled.

Every line of the resident file is read on every turn whether the turn
needs it or not, and the cost of growth is attention spread over more text.
So a section stays resident only when one of four tests holds:

1. the agent needs it before it can know to ask for it;
2. breaking it fails silently inside the turn;
3. it is a pointer whose trigger is not a phrase a user says;
4. a workflow skill reads it from there.

Anything else is a skill body, a tool description or the
doc that owns it. A commit that adds a section names the test it passes.
The sections that stay are the ones that fail with no error and no user
phrase to load a skill: the envelope tags, the delivery contract (only the
final text block leaves, a `<status>` owes an answer, a think-only turn is
silence, a mention must produce visible output), authority (a folder
isolates and confers nothing; the tool list is the grant set), the register
floor, the map of `/self`, the memory stores and storage, and rigor.

`tests/resident` holds the resident file to that shape.
`TestAgentFilePointersResolve` fails when a `/skill` or a `.md` the file
names does not exist, because a dangling pointer routes the agent to
nothing and no other check notices. `TestAgentFileCarriesTheCarveOut` fails
when one of the carve-out strings leaves the file.

## The system prompt

`buildSystemPrompt` (`src/index.ts:49`) joins the text layers in this
order: `SYSTEM.md`, the resident file for a harness that cannot read it
natively (codex, pi), then `PERSONA.md`. runed reads `SYSTEM.md` and `PERSONA.md`
off the group folder and ships them on the turn frame (`runed/render.go`).
When any layer exists, the joined text is the whole system prompt and the
harness's own preset is not used: for claude, a string `systemPrompt`
replaces the `claude_code` preset (`src/claude.ts:495`). When none exists,
the harness keeps its default. `SYSTEM.md` therefore replaces the base
prompt outright and nothing merges into it; a half-overridden prompt cannot
be debugged.

## The model gateway, per harness

`src/gateway.ts` reads `MODEL_GATEWAY_URL` and `MODEL_GATEWAY_KEY` once,
and each harness translates the pair into its own shape. An unset URL leaves
every harness unchanged.

| Harness | Redirect | Key |
| --- | --- | --- |
| claude | `ANTHROPIC_BASE_URL` in the SDK subprocess env (`claudeEnv`) | sent as `ANTHROPIC_AUTH_TOKEN` |
| codex | `-c model_provider=gateway` with `base_url=<url>/v1` and `wire_api="responses"`; codex rejects `chat` | `env_key=MODEL_GATEWAY_KEY`; with no key, `requires_openai_auth=true` sends codex's own ChatGPT bearer |
| pi | `registerProvider` re-points `anthropic` at the URL and `openai` at `<url>/v1` (`src/pi-extension.ts:115`) | the key is a fallback pi uses only when no real credential is present |

## The turn deadline

A turn has one deadline. `RUNED_RUN_TIMEOUT` (default 20m) is the run
ceiling. The group's `container_config` timeout lowers it, and the idle
timeout plus a grace period floors it (`runed/manager.go:408` `turnTTL`).
runed hands the guest a query timeout 30 seconds under that ceiling,
floored at 60 seconds, as `ARIZUKO_QUERY_TIMEOUT_MS`
(`runed/render.go` `queryTimeoutMs`), so the harness aborts and reports
before runed kills the guest. ant computes the deadline once, at start, and
every harness run in the guest aborts at it, the reply-owed nudge round
and the next IPC turn included. About 60 seconds before the deadline ant
puts a `[deadline]` note on the steer spool. The agent reads it as
`<user-steering>`, mid-turn through the steering hook and while it waits on
a task through the prompt stream. The note tells the agent to `reply` now and
to stop tasks it does not need with `TaskStop`. A run that starts inside
the last minute gets no note. The log line reads:

```
Query timeout: process deadline reached, aborting
```

The user loses the in-flight reply, and inbound messages that arrived
during the timed-out turn are tagged `errored=1` in `messages`. The next
inbound gets a fresh guest; the errored rows stay flagged and show as
failed turns in the dashboard.

Mitigations for skill authors and agents, with no platform change:

- **Split into multiple turns.** Emit partial results and ask the user for
  "continue" rather than packing a 20-minute job into one turn.
- **Checkpoint progress.** Write intermediate state to `~/facts/` or
  `~/tmp/` between subtasks, so a fresh guest resumes instead of restarting
  from zero.
- **Run fewer parallel subagents.** Every Task subagent shares the parent
  turn's wall budget; 8 parallel critics is 8 times the chance that one
  stalls the whole turn.
- **Background long jobs.** Pass `run_in_background: true` to the Agent tool
  for long work. The session stays open until the Agent reports and the
  model answers, or until the deadline. `TaskStop` plus a `reply` ends it
  early. A background shell does not hold the session: run it inside an
  Agent.
- **Stop agentic loops early.** Count the iterations and stop well before
  the cap. A loop that ran 28 of 30 iterations and saved its output is
  better than one that hit the deadline in iteration 30 and dropped
  everything.

## Ending the agent loop

ant polls `/run/ipc/input/` for steered messages between tool calls. An
empty file named `_close` in that directory ends the loop: ant deletes it
and stops waiting for further input (`src/index.ts:40`).

## Contracts with the Go side

Where Go produces a value and TypeScript reads it, a test reads both sources
and fails when either side moves. Each of these broke production once, with
no build failure and no log line.

| Contract | Go side | TS side | Test |
|---|---|---|---|
| spawn env keys | `runed/render.go` `turnEnv` | `process.env` reads in `src/` | `runed/env_contract_test.go` `TestSpawnEnvCarriesEveryKeyAntReads`, `TestSpawnEnvEmitsNothingAntIgnores` |
| `submit_turn` payload | `ipc.TurnResult` | `src/mcp.ts` | `tests/contract_ant_test.go` `TestSubmitTurnPayloadFieldsMatchAcrossLanguages`, `TestSubmitTurnDecodeKeepsEveryField` |
| `<status>` grammar | `router.ExtractStatusBlocks` | `src/index.ts` | `tests/contract_ant_test.go` `TestStatusGrammarAgreesAcrossLanguages` |

## Folder layout (agent view)

The guest mounts the group folder at `/home/node/`:

```
/home/node/
  PERSONA.md          operator-owned identity overlay
  CLAUDE.md           operator-owned runbook overlay
  .claude/
    CLAUDE.md         read-only core manual
    settings.json     runtime environment and permissions
    skills/           read-only packaged skills and writable custom skills
  diary/              session log
  facts/              researched knowledge
  users/              per-user memory
  workspace/          working files
  public_html/        bind-mount → /pub/<folder>/ (no auth)
  private_html/       bind-mount → /priv/<folder>/ (JWT)
```

Skills in `~/.claude/skills/<name>/` that match stock names in
`/opt/arizuko/ant/skills/<name>/` are managed (seeded + merged).
Custom-named skills are untouched.

## Skills

`scripts/curate-skills.sh` classifies every skill by its SKILL.md content:
arizuko-only when it mentions `@gated`, `@arizuko` or `gated.sock`,
portable otherwise. 92 are portable and 1 is arizuko-only, `mcp` (measured
at 7baffbb0). Every skill ships in `ant/skills/`; the partition moves
nothing.

## Build

```sh
make image      # docker build → arizuko-ant:latest (TS runtime)
```

The image is `FROM node:22-bookworm-slim`, installs `claude` CLI +
`socat` + runtimes (uv/bun/go/rust), copies `skills/` to
`/opt/arizuko/ant/skills/`, and sets `ENTRYPOINT ["node", "dist/index.js"]`.

`ant/Dockerfile` is the one inventory of what else the agent can reach —
chromium and `agent-browser` for the web, `ffmpeg` and `yt-dlp` for media,
`gh`, `git`, `jq`, `ripgrep`, `imagemagick`, `pandoc` and the build
toolchains. Read it there; a second list in prose drifts. Media the platform
receives is transcribed before the turn starts, by routd rather than by the
agent: `chanlib/README.md` § Inbound media is appended to the transcript and kept.

## Layout

```
ant/
  src/index.ts           Entrypoint (stdin → SDK → submit_turn)
  src/mcp-servers.ts     MCP server assembly: one server, alwaysLoad
  src/claude.ts          Claude Code harness driver + event normalization
  src/codex.ts, src/pi.ts  the other two harness drivers
  src/gateway.ts         the model-gateway pair every harness translates
  CLAUDE.md              the resident file (§ The resident file)
  PERSONA.md             Default persona
  skills/                Stock skills
  examples/              the product catalog `arizuko create --product` reads
  Dockerfile             Builds arizuko-ant:latest (TS)
```
