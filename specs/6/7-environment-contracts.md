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
