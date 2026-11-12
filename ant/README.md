# ant

The agent runtime for arizuko. Supports Claude Code, Codex and pi with the
same arizuko tools and explicit delivery. It supplies group identity, skills
and memory, and exposes MCP tools over routd's unix socket.

## What it does

Every turn runs in a fresh KVM guest booted from this image's exported rootfs.
`ant/src/index.ts` is the entrypoint: it reads stdin for the prompt,
renders the persona and package manual, connects the `arizuko` MCP server
via socat to `gated.sock`, runs the configured harness, and reports the outcome
via `submit_turn` to `routd`. Third-party connectors use that same gated socket.

## TS runtime (`src/`)

The shipping runtime declares `@anthropic-ai/claude-agent-sdk` in
[package.json](package.json), with the resolved dependency in [bun.lock](bun.lock). MCP servers are assembled in
`src/mcp-servers.ts`:

### The harness

`ARIZUKO_HARNESS` contains the group's complete harness document:
`{"backend":"codex","model":"gpt-6-astra","effort":"high"}`.
Ant validates the document before starting the adapter. Backend is required;
model and effort are optional. A present document replaces instance defaults
as a whole. Omitted optional fields use the adapter's native defaults.
Without the document, ant reads `ARIZUKO_BACKEND`, `ARIZUKO_MODEL` and
`ARIZUKO_EFFORT`. Pi writes native MCP configuration for the same routd socket.

ant does not implement an agent loop; it wraps one. `src/claude.ts`
drives Claude Code via `@anthropic-ai/claude-agent-sdk` — spawn the
harness, reduce its message stream to frames (`ClaudeTurn`), report the
turn over `submit_turn`. Only delivery tools send an answer; printed prose
stays in the transcript. A nonempty `nudge` response resumes the same turn ID
and continuity token before completion. Claude and Codex Stop hooks ask the
same `check_turn` decision. Pi uses the submission nudge through `submit_turn`.
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
`pi.ts` (`pi -p --mode json`, native skills and `builtin:mcp`).
`ARIZUKO_BACKEND` selects; unknown values fail the turn loudly. Skills
reach codex/pi through the load-time guard in `src/skills-scan.ts` — the
same skillguard table, a different hook point.

Any pi provider works, including a local model, through the operator's normal
provider setup and a `network_rules` row for the provider host. Custom native
configuration comes from a read-only mount at `/mnt/pi/models.json`.
Extension recipe in `EXTENDING.md` § "Swapping the agent harness".

- **`arizuko` core server** — socat bridge to `/run/ipc/gated.sock`,
  `alwaysLoad: true` so `send`/`reply`/`inspect_*`/`send_file` stay
  eager every turn.
- **Third-party connectors** — served over that SAME socket as arizuko
  tools (`ipc.StoreFns.Connectors`: built-in providers plus the
  operator's `connectors.toml`), so a connector call meets the grant
  check, the audit row and `CheckHold` like every other. A server the
  agent declares in `~/.claude/settings.json` is NOT read: that path
  handed tools straight to the harness and the agent writes that file
  itself. routd remains the tool authorization boundary.

No arizuko tool is deferred: one server, `alwaysLoad`, so a turn's tool
list holds every arizuko and connector tool the turn may call. Claude Code
defers its own `TaskStop`; the agent loads it with `ToolSearch`.

## The resident file

The turn renderer combines the selected packages' `CLAUDE.md` files, starting
with core's `ant/CLAUDE.md`, once at read-only `~/.claude/CLAUDE.md`.
Claude Code reads it as user
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
| pi | `registerProvider` re-points `anthropic` at the URL and `openai` at `<url>/v1` (`src/pi-extension.ts`) | the key is a fallback pi uses only when no real credential is present |

## The turn deadline

A turn has one deadline. `RUNED_RUN_TIMEOUT` (default 60m) is the run
ceiling (`runed/manager.go` `turnTTL`).
runed hands the guest a query timeout 30 seconds under that ceiling,
floored at 60 seconds, as `ARIZUKO_QUERY_TIMEOUT_MS`
(`runed/render.go` `queryTimeoutMs`), so the harness aborts and reports
before runed kills the guest. ant computes the deadline once, at start, and
every harness run in the guest aborts at it, the reply-owed nudge round
and the next IPC turn included. About five minutes before the deadline ant
puts a `[deadline]` note on the steer spool. The agent reads it as
`<user-steering>`, mid-turn through the steering hook and while it waits on
a task through the prompt stream. The note tells the agent to `reply`, end the turn, and
stop tasks it does not need with `TaskStop`. A run that starts inside
the last five minutes gets no note. The log line reads:

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
and fails when either side moves. The contract tests check both sources in one assertion.

| Contract | Go side | TS side | Test |
|---|---|---|---|
| spawn env keys | `runed/render.go` `turnEnv` | `process.env` reads in `src/` | `runed/env_contract_test.go` `TestSpawnEnvCarriesEveryKeyAntReads`, `TestSpawnEnvEmitsNothingAntIgnores` |
| `submit_turn` payload | `ipc.TurnResult` | `src/mcp.ts` | `tests/contract_ant_test.go` `TestSubmitTurnPayloadFieldsMatchAcrossLanguages`, `TestSubmitTurnDecodeKeepsEveryField` |
| provider refusal | `routd/api/v1.ProviderFailure` | `src/backend.ts` | `tests/contract_ant_test.go` `TestProviderFailureFieldsMatchAcrossLanguages` |
| per-turn usage record | `routd/api/v1.ModelUsage` | `src/mcp.ts` `ModelUsage` | `tests/contract_ant_test.go` `TestModelUsageFieldsMatchAcrossLanguages` |
| `submit_status` snapshot and reminder | `ipc.TurnStatus`, `ipc.TurnStatusAck` | `src/mcp.ts` | `tests/contract_ant_test.go` `TestSubmitStatusPayloadFieldsMatchAcrossLanguages`, `TestTurnStatusAckFieldsMatchAcrossLanguages` |
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

Packaged skills mount read-only at `~/.claude/skills/<name>/`.
Custom skills remain writable. Package selection and updates follow
[EXTENDING.md](../EXTENDING.md#installing-packages).

## Skills

`scripts/curate-skills.sh` classifies every skill by its SKILL.md content:
arizuko-only when it mentions `@gated`, `@arizuko` or `gated.sock`,
portable otherwise. Every skill ships in `ant/skills/`; the classification does not move files.

## Build

```sh
make image      # docker build → arizuko-ant:latest (TS runtime)
```

The image uses `node:22-slim` and copies the `ant/` build context to `/app`.
The build checks TypeScript and removes `dist`. `/usr/local/bin/ant` links
to `/app/src/index.ts`, whose shebang runs Bun. Guest init starts that executable
when the group selects the default handler.
`runed` mounts installed packages and selected skills read-only into the guest.

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
  Dockerfile             Builds arizuko-ant:latest (TS)
```

## Process interfaces and configuration

```text
runed stdin payload ──► ant ──► Claude Code or Codex harness
                         │
                         ├──► /run/ipc/gated.sock ──► routd MCP
                         └──► group home, transcripts, steering spool
```

ant is a per-turn process, not an HTTP daemon. It owns no store entities, resreg resources, metrics route, or health listener.
The process reads its first JSON input from stdin and receives later input through `/run/ipc/input/`.
MCP uses JSON-RPC over the routd Unix socket. `submit_turn`, `submit_status`, and `check_turn` are lifecycle calls, not delivery tools.
Only explicit delivery tools publish a reply. Standard output and harness text do not establish successful delivery.
runed supplies credentials and configuration; ant calls the model provider through the selected harness or gateway.

| Variable | Default | Meaning |
| --- | --- | --- |
| `ARIZUKO_HARNESS` | absent | Complete group harness JSON; replaces instance selection as a whole. |
| `ARIZUKO_BACKEND` | `claude` | Backend when no harness document exists: claude, codex, pi. |
| `ARIZUKO_MODEL`, `ARIZUKO_EFFORT` | native harness defaults | Model and effort without a group harness document. |
| `ARIZUKO_QUERY_TIMEOUT_MS` | `900000` | Process deadline in milliseconds; runed normally supplies its derived ceiling. |
| `ARIZUKO_IS_ROOT` | false | Literal `1` enables elevated prompt behavior; runed supplies it. |
| `ARIZUKO_GH_WRITE` | false | Literal `1` allows the guarded GitHub write path. |
| `ARIZUKO_PROGRESS` | false | Literal `1` forwards harness status text to routd. |
| `MODEL_GATEWAY_URL`, `MODEL_GATEWAY_KEY` | unset | Gateway base and per-turn virtual credential; a configured gateway needs a key. |
| `CODEX_HOME` | `/home/node/.codex` | Codex auth, rollout, and hook location. |
| `CLAUDE_CODE_OAUTH_TOKEN`, `ANTHROPIC_OAUTH_TOKEN`, `ANTHROPIC_API_KEY` | unset | Anthropic credentials from the rendered model profile. |
| `CODEX_API_KEY`, `OPENAI_API_KEY` | unset | OpenAI credentials from the rendered model profile. |

Pi maps `CLAUDE_CODE_OAUTH_TOKEN` to `ANTHROPIC_OAUTH_TOKEN` and `CODEX_API_KEY` to `OPENAI_API_KEY` only when the destination is absent.
`ARIZUKO_ASSISTANT_NAME` is read from generated Claude settings for conversation export naming, not from ant's process env.
Claude receives `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1` from the adapter.
Provider SDKs can consume their own environment variables; the table covers ant's explicit reads and credential mapping.
The fixed ant home is `/home/node`; changing HOME does not move its explicit paths.

## Timers and failure diagnosis

IPC input polling and Claude's task-wait steering polling each run every 500 ms.
The deadline note is scheduled five minutes before the process deadline; a run already inside that window gets no note.
Status reminders use the `reminder_after_ms` value returned by routd, not a second fixed timer.
A `_close` sentinel aborts the active query and stops further input waiting.
The steering spool is the batch-file directory `/tmp/arizuko-steer`; policy logs use `/tmp/arizuko-policy`.
Readers claim batches by rename; unclaimed batches stay for a later read.
Transcripts and exported conversations live in the group home. runed owns the temporary guest filesystem and persistent mounts.

A disconnected MCP socket prevents tools and terminal reporting. ant owns no durable result retry queue.
Provider errors can produce a structured `provider_failure` report; routd owns retry timing and the instance breaker.
Malformed harness JSON fails before a backend starts. Pi uses the same MCP boundary through its native mcp.json configuration.
Grep `submit_status failed`, `Query timeout: process deadline reached, aborting`, and `Close sentinel detected during query`.
There is no obs.Setup or OTLP exporter in ant; runed captures per-turn logs and routd records lifecycle results.
Model credentials remain readable inside a direct-mode guest ([BUGS L131](../BUGS.md)).
Local docs checks do not exercise a paid model turn; harness and contract tests provide separate behavior coverage.

Sources: [entry point](src/index.ts), [harness selection](src/backend.ts), [Claude](src/claude.ts),
[Codex](src/codex.ts), [pi](src/pi.ts), [gateway](src/gateway.ts), [rendered env](../runed/render.go).
