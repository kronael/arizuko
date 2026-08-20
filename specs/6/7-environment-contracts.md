---
status: draft
depends:
  [
    1-adoption-interop,
    16-daemon-standalone-matrix,
    ../5/9-agent-capability-eval,
  ]
---

# specs/6/7 — the two environment contracts

arizuko publishes an **environment**, and the value is what the environment
gives you for free. That is `6/1`'s corrected thesis, and this is the promise
each layer makes.

Two environments exist today and neither is stated as a contract. They are
prose in root `CLAUDE.md`, and nothing fails when they are violated — compare
`TestResourceName_NoStringLiteral`, which IS a contract because breaking it
breaks the build.

| Environment   | Who occupies it                                  | Conformance                    |
| ------------- | ------------------------------------------------ | ------------------------------ |
| **Agent**     | `ant` chassis: `claude` (default), `codex`, `pi` | `anteval` platform case set    |
| **Component** | a daemon or sibling library                      | a probe against a live process |

## Why this document exists

Three roadmap items are blocked on it, all for the same reason — nothing
declares what occupying a slot requires:

- **A second backend.** `ARIZUKO_BACKEND` existed, was never emitted as `-e` in
  the spawn args, and died silently. Nothing said what a backend must provide,
  so nothing noticed it provided nothing. (Closed 2026-08-20: §"The connector
  seam" below is the declaration, and `buildArgs` now emits the var.)
- **The substrate change.** Warm-per-group containers rewrite the turn
  lifecycle, and `16/1`'s durable streams rewrite it again. Without a contract
  each rewrite is a guess about what may move.
- **The channel layer as a shippable part.** `chanlib` has zero domain imports
  (`6/16`); the only thing keeping it in-tree is that its envelope is
  "whatever routd accepts" rather than a written contract.

## The agent contract

### Identity and authority

- The turn is **credentialed by the socket** — `SO_PEERCRED` on a per-folder
  MCP unix socket. There is no token to steal, and no token to forge.
- Authority is the **granted tool set**, never a coordinate. Depth confers
  nothing (`5/33`).
- Three tiers, and the grant model already expresses all three — this spec adds
  no new vocabulary:
  1. **Reads** — `inspect_*`, `find_messages`. Advertised unconditionally,
     containment applied per call.
  2. **The seeded `role:member` floor** — the twelve messaging verbs (`reply`,
     `send`, `send_file`, `send_voice`, `post`, `edit`, `delete`, `forward`,
     `quote`, `like`, `dislike`, `repost`), migration `0023`.
  3. **Everything else** — explicit delegation: routing, `network_*`, `acl`,
     invites, token mint, `register_group`, scheduling.

An author may rely on tiers 1 and 2 existing in every folder, and must degrade
gracefully without tier 3. A backend relies on none of it: the tool list is
discovered at runtime, so a backend implements the protocol, not the tools.

### Home

`/home/node/` persists across turns. `~/public_html` and `~/private_html` are
the two web slots; `~/diary`, `~/facts`, `~/users`, `~/workspace` and
`~/.claude` are never served at any URL. Anything outside the home is lost when
the turn ends.

### The envelope

The inbound payload IS the contract, not an implementation detail of `ant`:
`<messages>` with `<reply-to>` and `<attachment>` siblings, plus `<autocalls>`,
`<persona>`, `<observed>`, `<topic>` and `<link-context>`. A second backend
parses this. Defining a narrower payload for backend two would be a second wire
format for one consumer — the duplication this codebase spends its effort
deleting.

### Context — three layers, three time constants

| Layer                                | Owner       | Lifetime                | Where it comes from                                             |
| ------------------------------------ | ----------- | ----------------------- | --------------------------------------------------------------- |
| 1. transcript                        | the harness | one session, disposable | the backend's own session store                                 |
| 2. observed window                   | platform    | minutes                 | `<observed>` in the envelope                                    |
| 3. episodes / diary / work / persona | platform    | days–months             | group files, rendered by `prepareInput` (`container/runner.go`) |

arizuko does not own conversation history. The harness owns its transcript;
the platform owns the group. Layers 2 and 3 are rendered into the envelope by
Go before any backend runs: `<episodes>` from `groups/<folder>/episodes/`
(`container/episodes.go`), 14 days of diary, `<knowledge layer="work">` and
the persona, all in `prepareInput`. **A conformant backend must produce a
correct turn with layer 1 empty.** This is what makes a second occupant cheap:
a new backend replays nothing, because continuity across a harness swap is
carried by the group layer, which no backend owns.

### The connector seam

The contract is the process boundary — envelope on stdin, tools on the MCP
unix socket, `submit_turn` for every frame. Anything that speaks it replaces
the whole container occupant, in any language, with no arizuko code change.
That stays canonical; the `5/K` removal (CHANGELOG, migration `0023` era)
deleted an in-process `Backend` interface precisely because it duplicated the
wire protocol's genericity while being unreachable.

What ant ships now is not that interface back. It is a **connector chassis**:
the runtime half every occupant needs (stdin envelope parse, IPC steering,
idempotent `submit_turn`, `<status>` streaming) implemented once, with a seam
of one interface plus one file per backend for the harness half:

```ts
interface Backend {
  name: string;
  // undefined = start fresh. Layer-1 policy is the backend's own.
  resolveResume(sessionId: string | undefined): string | undefined;
  // true = the resume target was unusable; retry fresh, never deliver.
  isSessionError(subtype: string | undefined): boolean;
  spawn(cfg: SessionConfig, drain: DrainIpcInput): Session; // one turn
}
interface Session {
  events(): AsyncIterable<Event>; // system_init | assistant | tool_result | result | rate_limit
  close(): Promise<void>;
}
```

`ant/src/backend.ts` owns the types and the selector; `claude.ts`, `codex.ts`
and `pi.ts` each implement one backend. Selection is `ARIZUKO_BACKEND`
(`claude` | `codex` | `pi`), an instance `.env` infra toggle — static by the
dynamic-vs-static test, because a backend exists only if its CLI is baked into
the agent image. `container/runner.go buildArgs` emits it as real container
env (`-e`); the omission of exactly that emission is what killed the first
second-backend slot. An unknown value fails the spawn loudly — no silent
fallback to claude.

### Proposed: invert the seam — state what arizuko needs, not what claude does

**Status: proposal, unsigned.** The seam ships as described above. This section
records the redesign and its cost so it is decided once, not re-argued.

**The defect.** `ant/src/backend.ts` describes one implementation and lets the
others fit through it. Measured against the three occupants:

| `SessionConfig` field                       | claude | codex | pi  |
| ------------------------------------------- | ------ | ----- | --- |
| `prompt` `model` `cwd` `systemPrompt` `env` | ✓      | ✓     | ✓   |
| `resume`                                    | ✓      | ✓     | —   |
| `mcpServers`                                | ✓      | ✓     | —   |
| `resumeAt`                                  | ✓      | —     | —   |
| `addDirs`                                   | ✓      | —     | —   |
| `assistantName`                             | ✓      | —     | —   |
| `turnID`                                    | ✓      | —     | —   |

Four of eleven fields serve one occupant. `systemPrompt` carries claude's own
SDK union (`{ type: 'preset'; preset: 'claude_code' }`) inside the neutral type.
`DrainIpcInput` is handed to all three and used by one. And the clearest tell:
`isSessionError(subtype)` takes claude's result **subtype**, so wiring codex's
missing-rollout case meant **fabricating** `subtype: 'session_not_found'` — a
field codex does not have, invented to pass through a claude-shaped hole. When a
new occupant must invent a field to satisfy an interface, the interface is
describing an implementation.

**The principle.** The seam is derived from what arizuko needs of an occupant.
Every harness adapts to that; none of it adapts to a harness. `ant` IS the
abstract harness — the chassis that any coding agent drops into — so its seam
must read as arizuko's requirements, not as one SDK's options object.

**What arizuko actually needs.** Seven things, all already true today:

1. Exactly one terminal frame per turn, never silence.
2. Interim status frames, optional (the ⏳ message, `5/24`).
3. Continuity by **opaque token**: arizuko hands one out, gets one back, and
   never interprets it.
4. Tool calls cross the gated socket — arizuko's verbs, MCP over unix.
5. Steering, optional: pull queued user messages into a running turn.
6. Per-model usage, honest about what it cannot report.
7. A failed turn surfaces as that turn's result, never as a logged silence.

**The shape.**

```ts
interface Turn {
  prompt: string; // the envelope, untouched — it IS the contract
  cwd: string; // the group home
  model?: string; // backend-native id, verbatim
  continuity?: string; // opaque; undefined = fresh
  tools?: Record<string, McpServerConfig>;
  env: Record<string, string>;
}

type Frame =
  | { kind: 'continuity'; token: string }
  | { kind: 'status'; text: string }
  | {
      kind: 'result';
      ok: boolean;
      text?: string;
      usage?: Usage;
      retryFresh?: boolean;
    };

interface Harness {
  name: string;
  run(turn: Turn, steer: () => string[]): AsyncIterable<Frame>;
}
```

One method replaces four. `resolveResume` and `isSessionError` exist only so
`index.ts` can make decisions the harness is better placed to make; folding them
into `run` and `retryFresh` removes both the fabricated subtype and the
claude-shaped argument.

**Why this is richer, not merely smaller.** A continuity token is opaque, so it
holds every shape the three harnesses actually use:

- claude needs a **pair** (`sessionId` + a `resumeAt` message uuid);
- codex needs a **UUID** (its rollout thread id);
- pi needs a **path** (`--session <path>`).

The current `resume?: string` + `resumeAt?: string` cannot express pi's path
without a third field, and each new harness would add another. An opaque token
that the harness packs and unpacks costs the seam nothing and ends that growth.
This is the same rule as `L4`: identity is read from the credential, never
re-derived — here, continuity is read from the token, never re-parsed.

**What moves out.** `resumeAt` folds into the token. `addDirs`,
`assistantName`, `turnID` and the `claude_code` preset are claude wiring and
move into `claude.ts`, which reads them from the container input it already
receives. Nothing is lost; it stops being everyone's business.

**ant stays TypeScript — decided 2026-08-20, not to be re-argued.** The
question is real: two of the three occupants are already plain subprocess
connectors and need nothing from TS, and every other arizuko component is Go.
The blocker is exactly one occupant. `claude.ts` drives
`@anthropic-ai/claude-agent-sdk` in-process for three things a subprocess
connector does not get: `PreToolUse`/`PostToolUse` hooks as callbacks (which
carry the `5/23` skill-guard, the tool log and the `5/24` TodoWrite status),
mid-turn steering through a hook, and `interrupt`. The claude CLI does expose
`--print --output-format stream-json`, so a Go `ant` is reachable — it would
trade in-process hooks for settings-declared external hook commands. That trade
buys language uniformity and costs the write-time guard its directness. Not
worth it now. Revisit only if a second in-process SDK appears, or if the guard
moves out of the hook anyway (`L59`).

**Cost.** Three files rewritten against the new interface plus `index.ts`'s turn
loop, no behaviour change, and the existing tests are the safety net — they
assert argv and frame shapes, both of which survive. Do it before a fourth
occupant arrives, not after.

### Proposed: ant owns the capabilities, each harness renders the hooks

**Status: proposal, unsigned.** Companion to the seam inversion above; the two
ship together or not at all.

**The defect.** arizuko's per-turn capabilities live inside `claude.ts`, so two
of three occupants get none of them. Measured today:

| capability                    | where it lives now                   | codex         | pi                |
| ----------------------------- | ------------------------------------ | ------------- | ----------------- |
| skill guard (`5/23`)          | claude `PreToolUse` callback         | advisory scan | advisory scan     |
| tool log                      | claude `PreToolUse`/`PostToolUse`    | —             | —                 |
| mid-turn steering             | claude `PostToolUse` drain closure   | —             | —                 |
| `<status>` streaming (`5/24`) | runtime re-parses claude's raw shape | **broken**    | works by accident |

The `<status>` row is a live bug, not a gap: `index.ts` calls
`assistantText(event.raw)`, which reads `raw.message.content[]` — claude's
message shape. codex sets `raw` to `{item:{text}}`, so the parse returns empty
and **codex streams no interim status at all**, while pi works only because its
`message_end` payload happens to have the same nesting. The seam inversion fixes
this by making the runtime read `frame.text`, which every connector already sets.

**Every harness has the gates. This was measured, not assumed.**

| gate                | claude               | codex                                    | pi                                               |
| ------------------- | -------------------- | ---------------------------------------- | ------------------------------------------------ |
| block a write       | `PreToolUse` hook    | `$CODEX_HOME/hooks.json` `PreToolUse`    | `pi.on("tool_call")` → `{ block: true, reason }` |
| inject after a tool | `PostToolUse` hook   | `hooks.json` `PostToolUse`               | `pi.on("tool_result")` → returns new content     |
| turn boundaries     | `PreCompact`, `Stop` | `PreCompact`, `Stop`, `UserPromptSubmit` | `turn_start`, `turn_end`, `context`, `session_*` |
| register a tool     | MCP                  | MCP                                      | `pi.registerTool()`                              |

codex's `hooks.json` uses the SAME event names and the same
`{type:"command", command, timeout}` shape as Claude Code's settings hooks, so
one declaration renders to both with no translation. pi's hooks are in-process
TypeScript — and ant is TypeScript, so an ant extension imports the same policy
module rather than shelling out.

This retires two claims made earlier the same day and recorded here so the
correction travels with the text: codex was said to have no way to gate skill
loading (`L59` — it has `PreToolUse`), and pi was called the weakest occupant.
pi is in fact the most integrable: `pi.registerTool()` closes its MCP gap by
registering arizuko's verbs directly against the gated socket, `--mode rpc`
offers a bidirectional stdin/stdout protocol, and `AgentSession` is importable
in-process from the same npm package. Its only true gap was the MCP client, and
that gap has a door.

**The shape.** ant declares each capability ONCE as a policy module, plus a
table naming which event it binds to. Three renderers emit the harness-native
form:

- **claude** — SDK callbacks (today's `guardPreToolUse` / `guardPostToolUse`).
- **codex** — a generated `hooks.json` whose commands invoke a small ant CLI.
- **pi** — a generated extension file that imports the same policy module.

Mid-turn steering stops being a seam parameter. Today `DrainIpcInput` is passed
to all three occupants and used by one. Instead ant writes queued messages to a
file it owns and the `PostToolUse` binding reads it — the claude SDK already
works this way, so claude keeps its existing gate and merely reads the file
instead of a closure. `Harness.run` then needs no `steer` argument at all.

**Ownership: the system owns harness setup, the group owns its content.**
This is the rule the rest follows from, and today's layout breaks it. The group
dir mounts RW at `/home/node`, and harness configuration lives inside it —
`.claude/settings.json`, `.codex/hooks.json`, `~/.pi/agent/extensions/`. The
agent therefore authors the files that decide what it is allowed to do. Every
current defence is a patch on that inversion: `seedSettings` rewrites six keys
of `settings.json` every spawn (and `hooks` is not one of them), `auth.json`
and `config.toml` are read-only overmounted one file at a time, and
`strictMcpConfig` exists to make the SDK ignore a server map the agent can
write.

**`/opt/arizuko` is already the right home for it.** It mounts read-only from
the release image (`container/runner.go:559`) and already carries `ant/`'s
skills, `CLAUDE.md` and output styles. Harness setup is global setup — it is
versioned with the code, identical for every group, and never the agent's to
edit. It belongs there:

```
/opt/arizuko/harness/     (RO, image-baked)
  settings.json           claude
  hooks.json              codex — same schema, same event names
  pi-extension.js         pi — tools + guard + steering in one module
  policy/                 the ONE implementation the three bindings call
/home/node/               (RW, the group's own)
  diary/ facts/ users/ workspace/ public_html/
  .claude/skills/<custom>/  .claude/projects/  .codex/sessions/
```

Each harness is then pointed at the system copy by explicit path, with its own
discovery disabled so nothing the agent writes is ever consulted:

- **claude** — NOT `settingSources: []`. The SDK couples the two discoveries:
  "Must include `'project'` to load CLAUDE.md files" (`sdk.d.ts:1800`), so
  disabling settings discovery would also stop the group's `CLAUDE.md` being
  read. Keep `settingSources: ['project', 'user']` and make the FILE
  un-authorable instead — read-only overmount `~/.claude/settings.json`, the
  same mechanism as codex's hooks.json. That closes the `hooks` key without
  enumerating keys and without costing CLAUDE.md.
- **codex** — `/opt/arizuko/harness/hooks.json` read-only overmounted onto
  `~/.codex/hooks.json`, exactly as `auth.json` already is. Then
  `--dangerously-bypass-hook-trust` is safe: the trust ledger exists to prove
  provenance, and a read-only mount the agent cannot author proves it better.
- **pi** — `--no-extensions` plus `-e /opt/arizuko/harness/pi-extension.js`.
  pi's help is explicit that explicit `-e` paths still load when discovery is
  off, which is precisely the combination wanted.

**Discovery stays ON. Two kinds of discovery, only one is a hazard.**

- **Content discovery** — `CLAUDE.md` / `AGENTS.md`, skills, prompt templates.
  The group legitimately owns these, the agent may write them, and every
  harness must keep finding them. Nothing here is disabled.
- **Config discovery** — settings, hooks, extensions, MCP server maps. These
  decide what the agent MAY DO, so they must never be read from a path the
  agent can write.

The separating mechanism is therefore the MOUNT, not a flag: flags are too
coarse (claude's `settingSources` would take CLAUDE.md with it; codex's
`--ignore-user-config` was measured NOT to stop skill discovery, which is the
behaviour wanted). Every config path is overmounted read-only from a
system-owned source; every content path stays writable in the group dir and is
discovered normally.

System-owned has two tiers, and both are read-only to the agent:

1. **Image-baked, identical for every group** — `hooks.json`,
   `pi-extension.js`, the policy module. Ships in `/opt/arizuko/harness/`.
2. **Platform-generated per turn** — `settings.json`, which must carry
   per-group values (`ARIZUKO_GROUP_FOLDER`, `WEB_PREFIX`, `outputStyle`,
   `ARIZUKO_IS_ROOT`). runed writes it OUTSIDE the group dir and mounts it
   read-only in. `seedSettings` keeps computing exactly what it computes today;
   only its output path changes, so the agent stops being able to edit the
   result.

The group keeps everything that is genuinely its own: its custom skills, its
transcripts, its rollouts, its content. The test is authorship — if the agent
may edit it, it lives in the group dir; if editing it would change what the
agent is permitted to do, it is written elsewhere and mounted read-only.

**Evolution: the seam names requirements, each adapter DECLARES its mechanisms.**

The three harnesses move under us — codex gained a hook system, pi gained
skills and an RPC mode, claude gained plugins. A seam that hard-codes today's
mechanism goes stale silently, and a matrix maintained by hand goes stale
loudly: this document's own conformance table was wrong on three rows within a
day of being written (codex resume, codex's gate, codex `<status>`), every time
because a human asserted a capability instead of the code reporting it.

So the seam states only what arizuko REQUIRES, and each adapter declares how it
meets each requirement, as a value:

```ts
interface HarnessProfile {
  name: string;
  continuity: 'native' | 'none'; // can a turn resume the last one
  tools: 'mcp' | 'bridged' | 'none'; // how arizuko verbs arrive
  guard: 'write-time' | 'load-time' | 'none';
  steering: 'mid-turn' | 'between-turn';
  status: 'streaming' | 'terminal-only';
  // configPaths are the paths this harness reads that decide what the agent
  // MAY DO. runner.go overmounts exactly this list read-only. A harness that
  // adds a config file adds one entry here and the mount follows.
  configPaths: string[];
  // contentPaths stay writable and discovered — the group's own.
  contentPaths: string[];
}
```

Three things follow, and each removes a way to go stale:

1. **The mount list is derived, not hand-written.** `configPaths` IS what the
   platform makes read-only. A new codex config file is one profile entry, not
   a remembered edit in `runner.go`.
2. **The conformance matrix is a rendering of the profiles**, not prose beside
   them. It cannot claim a capability the adapter does not declare.
3. **A downgrade is a named value change.** This is `6/25` decision 3
   (`effectiveEgressEnforcement`) applied to harnesses rather than sandboxes:
   a capability that disappears shows up as `'none'` in a declared field and is
   logged, instead of a feature quietly not happening — the failure mode this
   repository keeps paying for (`L54` at INFO for eleven weeks, `L45`'s env var
   that stopped applying and looked healthy).

What the profile must NOT contain is a flag name. `--ephemeral`,
`--no-session`, `settingSources` and `--ignore-user-config` are implementation
detail of one adapter and change without notice; the profile records the
CAPABILITY, and the adapter's own file records how it currently obtains it.

**Order.** The seam inversion first — the `<status>` bug is live and the fix
falls out of it. Then the guard, which has the clearest security payoff and
turns `L59` from a hole into a shipped gate. Then the tool log and steering,
which are behaviour parity rather than new capability. Skill and package
management last: codex and pi both discover skills natively, so the question
there is which loader wins, and that deserves its own decision.

### Skills across backends — one scanner, two hook points

A skill is a named instruction file plus a trigger: `SKILL.md` with
frontmatter `name`/`description` (the Agent Skills format). The group's
installed set lives in `~/.claude/skills/`.

- **claude** — native discovery; the `5/23` skill-guard runs as a PreToolUse
  hook on writes into `~/.claude/**`. The gate sits at WRITE time.
- **pi** — implements the Agent Skills format natively; the connector passes
  each skill dir via `--skill`. No hook mechanism.
- **codex** — no skill mechanism; the connector renders an index
  (name — description — path) into the prompt, and the model reads the
  `SKILL.md` on match.

codex and pi cannot run the write-time hook, so the connector moves the SAME
scanner (`skillguard.ts`, the hermes pattern table — one table, never a
second copy) to LOAD time: at session start every skill is scanned, and a
skill with blocking findings is excluded from pi's `--skill` list and from
codex's index. Stated honestly: load-time scanning gates what the connector
loads, not what the model reads by path afterwards — it is narrower coverage
than the write-time gate, not parity. The bash secret scrub
(`guards.ts`) also does not run on codex/pi; the credential it scrubs
(the Anthropic OAuth token) is not present in those backends' env, which
shrinks but does not erase that gap.

### Conformance matrix (2026-08-20)

| Capability                             | claude                   | codex                                          | pi                                                                     |
| -------------------------------------- | ------------------------ | ---------------------------------------------- | ---------------------------------------------------------------------- |
| envelope parse (prompt in)             | yes                      | yes                                            | yes                                                                    |
| terminal frame via `submit_turn`       | yes                      | yes (connector)                                | yes (connector)                                                        |
| arizuko MCP tools (`reply`/`send`/…)   | yes                      | yes (`mcp_servers` → socat)                    | **no** — pi has no MCP client; the terminal frame is its only delivery |
| mid-turn steering (IPC → active query) | yes (PostToolUse drain)  | no — between-turn only                         | no — between-turn only                                                 |
| interim `<status>` streaming           | yes                      | yes (`agent_message` events)                   | yes (`message_end` events)                                             |
| session resume (layer 1)               | yes (`--resume`)         | yes (`exec resume <thread_id>`)                | available (`--session <path>`), NOT wired                              |
| skills                                 | native                   | native ($HOME) + a redundant connector index   | native (`--skill`)                                                     |
| skill-guard (`5/23`)                   | write-time hook          | load-time scan                                 | load-time scan                                                         |
| per-model usage accounting             | tokens + cost            | tokens only, cost 0                            | tokens + cost                                                          |
| model credential                       | subscription OAuth / env | `~/.codex` or `OPENAI_API_KEY`/`CODEX_API_KEY` | provider API key env                                                   |

Fresh-each-turn is conformant: the contract requires correctness with layer 1
empty, not layer 1 at all. A backend without MCP reach passes only the cases
whose observable effect its own tools (shell, files, egress) can produce —
the matrix row IS the expected anteval delta, and a truthful delta beats a
parity claim.

**Conformant is not the same as correct, and this matrix said "no" where it
meant "we did not wire it."** Measured against codex-cli 0.147.0 and pi 0.51.6
on 2026-08-20:

- **codex resumes.** A persisted turn returns a `thread_id`; `codex exec resume
<id>` continues that rollout and recalled a word given only in the prior turn,
  reading 29,184 cached input tokens. The connector passed `--ephemeral`, which
  writes no rollout, so there was nothing to resume — a connector choice stated
  as a harness limit. Now wired: `resolveResume` returns the stored id, and a
  missing rollout retries fresh through the runtime's existing `sessionError`
  path. codex exits **0** with an empty stdout when the rollout is gone, so the
  signal is the stderr text, never the exit code.
- **pi has sessions too** — `--session <path>`, `--session-dir`, `--continue`,
  `PI_CODING_AGENT_DIR`. The connector passes `--no-session`. Unwired, not
  unavailable; `--session <path>` is the better shape here, because routd hands
  out the id and a path needs no resumable-UUID contract (the `L54` class).
  **Certified in a container, 2026-08-20.** The rebuilt `arizuko-ant` image
  (codex-cli 0.148.0) with the group dir bind-mounted at `/home/node` and
  `ARIZUKO_BACKEND=codex`:

1. Turn 1 minted `01a02014-484c-7792-b7b7-8c6c40f586dd` and wrote
   `.codex/sessions/2026/08/20/rollout-…-01a02014-….jsonl` to the HOST mount.
2. Turn 2, given that id, resumed it and answered with a word supplied only in
   turn 1 — so the rollout survives between spawns, which is the property the
   bind mount exists for.
3. Turn 3, given an id with no rollout, produced `subtype=session_not_found`,
   retried fresh through the runtime loop, and delivered the new turn's answer.
   The harness diagnostic never reached the user.

The stream shape is stable across the version skew between host (0.147.0) and
image (0.148.0); both were exercised. `submit_turn` was the only failure in
that harness, because the bare `docker run` has no gated socket — the MCP path
is unchanged by this work and is covered by the `mcp_servers` overrides.

- **codex discovers skills natively** from `$HOME/.claude/skills`, which in the
  container IS the group's skills dir (`runner.go:552` mounts the group at
  `/home/node`). Confirmed by a skill placed in `cwd/.claude/skills` NOT being
  loaded while every `$HOME` skill was. The connector's prompt index therefore
  re-does what codex already does — and, worse, the load-time guard cannot gate
  what codex loads, because codex has no flag to restrict discovery. Recorded
  as `L59`; the index stays until that is decided, since deleting it would
  remove the only place the guard's verdict is visible.

### Delivery — idempotent everywhere, not just at the end

**Every message and every MCP call carries the guarantee.** This is the part
most easily got wrong, and getting it wrong produces a contract with a special
case that does not belong in it:

- every inbound message has a stable id, and that id **is** the idempotency key
  (`routd/server.go` collapses `X-Idempotency-Key` into `messages.id`);
- every outbound send claims its key before the platform call and settles after
  (`chanlib.deliverOnce`), so a send is applied **at most once** and an
  unsettled claim is refused rather than guessed;
- an agent-authored frame dedups on its own key (`turn_results`).

**The terminal frame is not special here.** It looks special because the SDK
and `pi` treat "the turn is finished" as a distinct event in their own control
flow — that is a property of those harnesses, not of arizuko. In this contract
a terminal is simply the last frame, subject to the same rule as every other:
delivered at least once, applied at most once.

### The lifecycle seam — deliberately not frozen

Two roadmap items rewrite how a turn starts and ends. The contract therefore
states only what survives all three shapes:

- a turn is credentialed by the socket;
- it carries a stable key end to end;
- the agent appends frames;
- every frame is delivered at least once and applied at most once.

Per-turn ephemeral containers satisfy this today. A warm container serving N
turns satisfies it. A durable event log with replay satisfies it — replay is
re-reading frames whose application already happened. **Durable turns are an
implementation of this contract, not a break of it.** The version bumps only if
one of those four sentences becomes false.

### Conformance: `anteval`, per backend

A backend is arizuko-compatible to the degree it passes the platform case set
(`5/9`, `smoke = true` marking the gate basis). This replaces a prose list of
required-versus-optional behaviours: what is required is what the cases
assert, and the score is the compatibility statement. Certification is the
same set run once per backend, by hand, against an instance whose `.env` sets
that backend:

    ARIZUKO_BACKEND=codex  # instance .env; restart; then, manually:
    anteval run --cases ./anteval/cases ...

anteval never runs automatically — no CI, no cron; `make -C anteval validate`
(catalog validation, no live run) is the only pipeline-safe step. The
`fresh-context` case asserts the layer-1-empty guarantee directly: `/new`
resets the session in the same inbound, so the turn that fires the callback
ran with an empty transcript. **As of 2026-08-20 only `claude` has a live
anteval history; `codex` and `pi` connectors run a turn (recorded-fixture
tests + a local CLI invocation) but are not yet live-certified.** The matrix
above states what each backend can pass in principle; the live run per
backend is the operator's step.

## The component contract

The eight rules a daemon owes — own and migrate your own schema, listen on
`:8080`, `obs.Setup` in `main()`, register a management entity as a resource,
bind every `jid`/`folder`/`run_id` to the caller, ship a compose fragment, mount
a public `/openapi.json`, write the audit row in the mutation's own tx — are
stated in root `CLAUDE.md` and are NOT restated here. They were, and a table
that duplicates a rule is a second copy to drift. `CLAUDE.md` is the one a
developer reads; this spec says who the rules are FOR and how conformance is
checked.

### Who this contract is for — capabilities federate, infrastructure conforms

Corrected 2026-08-17, because the earlier framing oversold it. **A stranger
adding a capability does not use this contract and should not.** `5/13` ships
that path already: the service runs anywhere, in any language, on anyone's
infra; arizuko holds the credential, the agent calls a tool by name, an `acl`
row decides whether this folder may call it, and the call is audited. Federating
a credential is the right answer for a capability, and it is strictly more
flexible than conforming.

This contract is for parts in the **data path or the trust path**, where "remote
service holding a token" is not a weaker variant of the design but a broken one.
crackbox is the case: it is the egress proxy, every container's traffic goes
through it, and it is what _enforces_ the per-folder allowlist. A remote
crackbox is defeated by anything that can reach the network without presenting
the token — and the thing being contained is exactly the thing with network
access. A filter you can route around is not a filter. Same for `mcpfw`
(filters MCP calls in transit) and `authd` (the trust root).

So the honest scope is arizuko splitting **its own** parts out without each
split reinventing auth, audit and deployment. That is worth doing. It is not,
by itself, an adoption strategy, and `6/1`'s plan item — get _a_ stranger, one,
running crackbox — is the measure that matters.

### Conformance

An in-repo daemon is checked by Go tests. Those read the source, so they check
the **code**. `arizuko probe <base-url>` (`cmd/arizuko/probe.go`, shipped
2026-08-17) checks the **deployed instance**, which no test can: a daemon passes
every test and still ships with `openapi.json` behind auth or a gate unwired.
Read it as an operator's deployment check first, and a foreign component's
conformance check second — that second use is real but rare, per the scope
above.

| Check                                  | What a failure means                                                                    |
| -------------------------------------- | --------------------------------------------------------------------------------------- |
| `openapi.json` answers unauthenticated | it is mounted behind auth, so nothing can discover the component                        |
| the document is OpenAPI 3.x            | not a contract any consumer can read                                                    |
| every advertised path is served        | the doc names a resource the mux does not mount — a published lie                       |
| non-public paths require auth          | a `/v1` resource ships ungated                                                          |
| a foreign folder is refused            | the handler never binds its folder param to the caller (`--token` + `--foreign-folder`) |

**What the probe cannot see, stated so a pass is not over-read**: it detects
paths the doc advertises and the mux does not serve, never the reverse.
Deriving the mounted SET by probing is the mistake `F33` records — proxyd
answers every path from a `/` catch-all, so "it responded" never proves "this
resource is mounted here". That direction stays a Go test
(`resregtest.AssertServesNoForeignResource`).

An **empty** document is conforming, not a failure: `dashd` mounts only
`/dash/` pages and owns no `/v1` resource, so it truthfully advertises nothing.
A component owes an honest doc, not a non-empty one — the probe's first live run
failed `dashd` on that and the check was corrected, not the daemon.

The audit-row half of this table is not yet probed: it needs the component to
expose its own `audit_log` as a resource, which is not required of it today.

## Out of scope

The tool list itself (grants already express it), per-vendor credential
mechanics (a credential proxy — declined 2026-08-20), and the substrate's reap policy (its own decision). This
spec says what must stay true across those, not how any of them works.

## Ties

`6/1` adoption thesis · `6/16` the four layers and where `ant` sits ·
`5/9` anteval · `5/33` grants · `5/P` runed · `5/34` channel protocol ·
`16/1` durable streams.
