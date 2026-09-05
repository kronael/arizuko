---
status: draft
depends:
  [
    7-environment-contracts,
    ../4/P-personas,
    ../5/Y-output-styles-per-surface,
    ../8/A-hierarchical-skills,
  ]
---

# specs/6/34 — the resident budget: what an always-loaded file may carry

A proposal, not a decision. The tables below describe each file as the
rule leaves it; nothing moves until this spec is `planned`.

## Problem

Two files load without being asked for, on every turn, for a reader who
never chose them:

- `ant/CLAUDE.md` — 388 lines, 33,921 bytes, 38 sections. Seeded into
  every group as `~/.claude/CLAUDE.md` (`container/runner.go:1246`), which
  Claude Code reads on every turn because `settingSources` is
  `['project', 'user']` (`ant/src/claude.ts:473`) and the turn's cwd is
  the home (`ant/src/index.ts:243`).
- `CLAUDE.md` at the repo root — 579 lines, 28,733 bytes, 40 sections.
  Read by every maintainer session and every subagent opened in the repo.

The premise "the root file loads in every group container" is false and
the correction matters: no code path copies the root file anywhere near a
container. The file an agent lives with is `ant/CLAUDE.md`. Both files
are nonetheless the same problem — an unasked-for read that has grown by
accretion (124 commits to `ant/CLAUDE.md`; 44 in April 2026, 45 in May) —
so one rule covers both, applied to each file's own reader.

The maintainer's position is that an always-loaded file is a map: it
teaches the reader how to find the right skill and where things live, and
the contents load on demand. arizuko is the harder case, because an agent
cannot discover some things from a map at all: it needs the turn envelope,
the delivery contract and the authority model in hand before it can act,
and none of those suggests a skill name. The spec's job is the line
between the two.

### What one turn actually carries

The platform file is not the whole budget. Measured on krons on
2026-09-05 (`/srv/data/arizuko_krons/groups/*`), for a claude-backend
group, before the conversation itself:

| Resident text                                                 | Bytes                                   | Source                                                                                                         |
| ------------------------------------------------------------- | --------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `~/.claude/CLAUDE.md` (this spec's subject)                   | 31,048–34,278 across 10 groups          | `ant/CLAUDE.md` seeded once, then 3-way merged on `/migrate`; three distinct live sizes = operator edits exist |
| `~/CLAUDE.md` (product regions, `5/28`)                       | 0 on five groups; 31,134–43,734 on four | `container/blend.go:441` concatenates each product's `CLAUDE.md`                                               |
| skill listing (name + description + when_to_use, ≤1,536 each) | ≈30,800 for the 93 stock skills         | Claude Code preloads every `~/.claude/skills/*/SKILL.md` frontmatter; live groups hold 84–116 skill dirs       |
| MCP tool catalog                                              | 60 tools, ≥5,277 of description text    | `alwaysLoad: true` on the arizuko server (`8/A`); the live JSON schemas add parameter text                     |
| output style                                                  | 952–1,492                               | `ant/src/index.ts:47` `buildSystemPrompt` = `SYSTEM.md` + the selected style                                   |
| `/resolve` body                                               | ≈2,700                                  | the `[resolve]` nudge (`container/runner.go:531`) invokes it on every prompt, so it is resident in practice    |
| envelope                                                      | variable                                | `routd/prompt.go:35`: system messages, `<autocalls>`, `<persona>` summary, `<observed>`, `<topic>`, messages   |

Fixed text before the first user word is therefore ≈71–115 KB, and the
platform file is 30–48 % of it. Tokens are not measured on this host (no
tokenizer installed); bytes ÷ 4 is the working estimate, so the platform
file alone is ≈8,500 tokens per turn.

A maintainer session has the same shape: root `CLAUDE.md` 28.7 KB +
`~/.claude/CLAUDE.md` 14.6 KB + `MEMORY.md` 23.3 KB + the listing of 86
global and 2 project skills. The root file is ≈40 % of that fixed text.

### The second copy is already there

`ant/skills/self/` is a 15-file, 39,820-byte directory whose `SKILL.md`
is a dispatch table — the map this spec wants — and whose siblings carry
the same content the resident file carries:

| `ant/CLAUDE.md` section    | Sibling that already owns it                  |
| -------------------------- | --------------------------------------------- |
| Agent home is your kingdom | `self/storage.md` quotes it verbatim          |
| Web routing and auth       | `self/web-routing.md` — the same proxyd table |
| Runtimes                   | `self/runtimes.md` — the same five bullets    |
| Autocalls                  | `self/autocalls.md`                           |
| Gateway commands           | `self/commands.md`                            |
| Additional mounts          | `self/workspace.md`                           |
| Tenancy (env vars)         | `self/identity.md`                            |
| Storage table              | `self/storage.md`                             |

That is the "one declaration, many readers" rule of root `CLAUDE.md`
violated in the agent's own manual: two copies, and the live sizes show
they drift (`6/7` §"The component contract" states the same cost for its
own tables). The root file has the same shape: `specs/CLAUDE.md` opens
with "the settled rules from root `CLAUDE.md` — repeated here as a
checklist", `template/web/CLAUDE.md` owns "Deploy to live krons" while
the root carries 41 lines on the same subject, and
`ant/skills/self/migrations/CLAUDE.md` owns the `MIGRATION_VERSION` steps
the root's "Shipping changes" restates.

## The rule

A section is **resident** — it stays in the always-loaded file — only if
at least one holds:

1. **Needed before the reader can know to ask.** The reader must obey it
   before it has a question a skill name could answer: how the turn
   arrives, what is delivered, when to stay silent, what a folder confers.
   Test: could the reader reach this fact by name via `/self`, the tool
   list, the output style, or the doc that owns it? If yes, it is not
   resident.
2. **Breaking it fails silently inside the turn.** A lost reply, a
   `<think>`-only turn on a mention, a file written to `/tmp`, a fabricated
   number: no error reaches the reader, so nothing prompts a lookup. A
   silent failure earns residency **until a mechanism makes it loud** (the
   "fail loud" discipline of root `CLAUDE.md`); when the mechanism ships,
   the line leaves. A failure the reader sees — a 403, `ErrUnsupported`, a
   missing tool, a 404 on `curl` — is its own pointer, and the rule that
   explains it lives on demand.
3. **A pointer whose trigger is not a user phrase.** Skill frontmatter
   routes on what users say; an envelope tag, a mount state or an error
   code is not something a user says, so no skill description catches it.
   Such a pointer is one row: trigger → owner. It is never the content.

Everything else is one of: a skill body (its frontmatter is the pointer),
a tool description (`tools/list` is the reader), an output style (one loads
per session, so per-surface rules belong there — `5/Y`), or the document
that owns the subject. A table of topics → skills is not a map and does not
go in the resident file: the skill listing already is that table, and a
hand copy goes stale.

The rule applies to a new section the same way: name which of the three
tests it passes in the commit that adds it; a section that passes none is
a skill.

## `ant/CLAUDE.md`, section by section

Sizes are lines / bytes of the current file. "Keep" means resident under
the rule, with the named trim; "move" names the owner and the resident
remainder, if any.

| Section                             | L / B     | Verdict            | Rule | Destination and remainder                                                                                                                                                                                                                                                                       |
| ----------------------------------- | --------- | ------------------ | ---- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Identity                            | 5 / 168   | keep               | 1    | who/where in two env vars; the introspection recipe is `self/identity.md`                                                                                                                                                                                                                       |
| Response Style                      | 21 / 2320 | keep               | 2    | the register floor; `4/P` decides it lives here because one output style loads per session. The STE bullet list is the only trim candidate (open question 6)                                                                                                                                    |
| Rigor                               | 4 / 406   | keep               | 2    | fabrication has no error                                                                                                                                                                                                                                                                        |
| Tenancy model                       | 12 / 1861 | keep, trim         | 1    | resident: group = isolated home, path carries zero authorization, tool list = grant set, topic in one line. The `register_group` paragraph moves to `self/mcp.md` — the tool's own error says "call this"                                                                                       |
| Autocalls                           | 4 / 269   | keep               | 1    | envelope; `self/autocalls.md` holds the long form                                                                                                                                                                                                                                               |
| How messages arrive                 | 24 / 1306 | keep               | 1    | the envelope is the contract (`6/7`); nothing else names `<messages>` and `<reply-to>`                                                                                                                                                                                                          |
| When to respond                     | 18 / 1638 | keep               | 2    | silence on a mention, final-block-only delivery (`L149`), `<link-context>`: all silent. Already shaped right — it points at `chat-link.md` for the full rules                                                                                                                                   |
| Greetings                           | 4 / 70    | move               | —    | `hello`'s frontmatter already routes on "hi/hello with no specific task"; the resident line is a second copy                                                                                                                                                                                    |
| Resolve                             | 4 / 634   | keep, trim         | 3    | the nudge is the mechanism (`runner.go:531`); one resident line names it. "Read the channel before answering `no context`" is `resolve/SKILL.md` §2 already                                                                                                                                     |
| Task discipline                     | 5 / 408   | keep               | 2    | `AskUserQuestion` ends the turn silent. Leaves when a PreToolUse deny rule for it ships (`6/7` policy table)                                                                                                                                                                                    |
| Skills and tools                    | 6 / 642   | keep               | 3    | this is the map: `/self`, and connector tools deferred behind Tool Search — the deferral is invisible without the line                                                                                                                                                                          |
| Model economy                       | 4 / 461   | move               | —    | `sonnet` and `haiku` frontmatter route on scouting and mechanical lookups; economy is not a silent failure                                                                                                                                                                                      |
| Memory stores                       | 8 / 388   | keep               | 3    | four rows: event → skill. The `/issues` line joins it as a fifth row                                                                                                                                                                                                                            |
| Recording user-reported issues      | 4 / 103   | move               | —    | one row in Memory stores                                                                                                                                                                                                                                                                        |
| Status updates                      | 13 / 1331 | keep, trim         | 2    | ⏳ forever is silent. The `TodoWrite` paragraph becomes one line; `5/24` owns the rendering                                                                                                                                                                                                     |
| Persona                             | 9 / 834   | move               | —    | the `<persona>` block carries its own pointer, "(For full register: /persona)" (`routd/prompt.go:138`); `persona/SKILL.md` owns the layers. Layer 1 is also a stale claim (open question 2)                                                                                                     |
| Tool discipline                     | 4 / 506   | merge              | 2    | folds into When blocked — the same "enumerate before declaring absent" rule twice                                                                                                                                                                                                               |
| When blocked                        | 15 / 1602 | keep, trim         | 2    | resident: the three-step order. The `inspect_*`/`find_messages` paragraph is tool-description territory (`self/mcp.md`); the "check your live tool list" paragraph repeats Tenancy; the session-opening ritual is `self/SKILL.md` §"Session recovery"                                           |
| Environment (`$WEB_HOST`)           | 4 / 143   | keep               | 2    | a literal `$WEB_HOST` in chat raises no error                                                                                                                                                                                                                                                   |
| Network egress                      | 15 / 2087 | move, 3-line stub  | 2→3  | the 403 is visible, the misreading is what `egress-diagnose`, `egress-dns-distinguish` and `self-grant-noop` in `anteval/cases/anteval.toml` test. Owner: a new `self/egress.md` row in `self`'s table. Resident: default-deny; a host that 403s on every path is the allowlist; `/self` egress |
| Agent home is your kingdom          | 8 / 645   | move               | —    | `self/storage.md` holds it verbatim                                                                                                                                                                                                                                                             |
| Publishing a web page               | 39 / 3114 | move, 1-line stub  | —    | the largest section; failures are 404/401/EACCES, and `priv-401`, `priv-deny`, `priv-grant`, `web-route`, `spawn-publish-reach` test the behaviour. Owner: `web/SKILL.md` (the slots) + `self/web-routing.md`. Resident: verify the URL before you announce it                                  |
| Storage — persistent vs transient   | 16 / 903  | keep               | 3    | this table is the map (path → purpose → skill); `self/storage.md` keeps the prose. Gains one row: `/tmp` is lost at turn end                                                                                                                                                                    |
| Additional mounts (`/mnt/`)         | 14 / 889  | move               | —    | operator SQL the agent cannot run; `self/workspace.md` owns the mount table                                                                                                                                                                                                                     |
| CLAUDE.md ownership                 | 10 / 936  | move               | —    | matters only during `/migrate`; `self/migration.md` owns seeding and the 3-way merge                                                                                                                                                                                                            |
| Web routing and auth                | 20 / 1506 | move               | —    | `self/web-routing.md` holds the same table                                                                                                                                                                                                                                                      |
| Gateway commands                    | 11 / 368  | move               | —    | `self/commands.md`; "when asked for help, mention these" is `hello`'s third part                                                                                                                                                                                                                |
| `@<unknown>` prefix                 | 4 / 418   | move               | —    | `self/chat-routing.md`; the message reaches the agent as visible text                                                                                                                                                                                                                           |
| Runtimes                            | 8 / 460   | move               | —    | `self/runtimes.md` holds the same list; bare `pip` fails loudly                                                                                                                                                                                                                                 |
| Inbound media attachments           | 13 / 713  | keep, trim         | 1    | resident: the `<attachment>` shape and "no tag = the file did not arrive" (`read-attachment-honesty` tests it). The transcript and PDF notes move to `self/messages.md`                                                                                                                         |
| Social actions                      | 11 / 1515 | move               | —    | `reply`'s description already says "THE DEFAULT way to respond" in 674 characters the agent reads every turn; the `platform_id` and `ErrUnsupported` rules belong in the verbs' descriptions, else `channels`                                                                                   |
| Slack threading / Discord threading | 8 / 821   | move               | —    | per-surface: `slack-thread.md`, `slack-channel.md`, `discord*.md` output styles (`5/Y`)                                                                                                                                                                                                         |
| Reactions                           | 8 / 1079  | keep 1 line, move  | 2    | resident: a reaction is the floor when a reply is expected (silence is silent). Register mirroring and DM anti-patterns → the `*-dm.md` styles                                                                                                                                                  |
| Telegram groups                     | 4 / 516   | move               | —    | `telegram-group.md` output style                                                                                                                                                                                                                                                                |
| Delivering files to users           | 4 / 458   | keep 1 line        | 2    | inlining a file is not an error; the caption rule belongs in `send_file`'s description                                                                                                                                                                                                          |
| Local paths vs public URLs          | 18 / 1409 | keep 2 lines, move | 2    | resident: the user cannot open container paths — translate first (`link-is-public-url` tests it). The translation table → `self/web-routing.md`                                                                                                                                                 |
| Response size + medium              | 9 / 994   | keep, trim         | 2    | exactly what `5/Y` specifies: a pointer at the style plus the surface-agnostic long-answer pattern. The example paragraph is the trim                                                                                                                                                           |

Summing the "keep" column with its trims: ≈15 KB stays resident, ≈19 KB
leaves, and ≈12 KB of what leaves already exists at its destination. The
carve-out — envelope, delivery, authority, register, the map — is ≈10 KB
of the 15 and does not shrink under this rule.

## Root `CLAUDE.md`, section by section

The reader is a maintainer session. Its on-demand surfaces are the global
skills, the two project skills (`.claude/skills/eval`, `create-eval`), and
directory-scoped `CLAUDE.md` files that Claude Code loads when a file in
that directory is read — `specs/CLAUDE.md`, `template/web/CLAUDE.md`,
`ant/skills/self/migrations/CLAUDE.md` are the repo's own precedent for
on-demand rules. The root file has no conformance harness, so this table
moves **explanations, never rule lines**: every invariant keeps its one
sentence resident and sends its cost narrative to the spec or bug that
recorded it.

| Section                               | L / B     | Verdict                | Rule | Destination and remainder                                                                                                                                                                                                                                                                        |
| ------------------------------------- | --------- | ---------------------- | ---- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| Identity is configured, never derived | 4 / 466   | keep                   | 2    | derivation looks right until the container moves                                                                                                                                                                                                                                                 |
| Canonical paths                       | 12 / 587  | keep, trim             | 1    | module path before any import. The rename history line is `CHANGELOG.md`'s                                                                                                                                                                                                                       |
| Response Style                        | 7 / 291   | keep                   | 2    | the project override of the global register                                                                                                                                                                                                                                                      |
| Minimality and orthogonality          | 86 / 5422 | keep rules, move costs | 2    | eight rule sentences stay (≈25 lines). Each "Cost:" narrative goes to its owner — `5/8` (manifests, `L44`/`L45`), `5/17` + `5/33` (auth middleware, wire identity), `BUGS.md` already holds the rest. `specs/CLAUDE.md` §"Architectural invariants" then points at the root instead of restating |
| Essence                               | 11 / 588  | merge                  | —    | it says "read `README.md`"; that is a row in When to read what                                                                                                                                                                                                                                   |
| Build & Test                          | 18 / 586  | keep                   | 1    | the commands before any action                                                                                                                                                                                                                                                                   |
| Architecture / Core vs integrations   | 13 / 517  | move                   | —    | `ARCHITECTURE.md:3` is titled "Core vs integrations"                                                                                                                                                                                                                                             |
| Discoverability                       | 25 / 1339 | move, 1-line rule      | —    | `resregtest.AssertServesNoForeignResource` is the detection, so the explanation lives in `resreg/README.md` + `5/8`. Resident: every daemon mounts `/openapi.json`, engine-emitted                                                                                                               |
| Observability                         | 18 / 832  | move, 1-line rule      | —    | `ARCHITECTURE.md:849` + `obs/`. Resident: `defer obs.Setup(...)()` in every `main()`                                                                                                                                                                                                             |
| Docs layout / When to read what       | 26 / 1312 | keep, grow             | 3    | this IS the map; it absorbs Essence, Layout, Service Architecture, Entrypoint, Migrating from kanipi as rows                                                                                                                                                                                     |
| Updating the web docs                 | 41 / 1869 | move                   | —    | `template/web/CLAUDE.md` owns "Deploy to live krons" and loads when the tree is touched; the "visuals are load-bearing" paragraph moves with it. Resident: one row                                                                                                                               |
| Layout                                | 7 / 293   | merge                  | —    | a row                                                                                                                                                                                                                                                                                            |
| Refine scope                          | 20 / 937  | move                   | —    | a project skill, `.claude/skills/refine/SKILL.md` (open question 5 on the name)                                                                                                                                                                                                                  |
| Conventions                           | 39 / 2311 | split                  | 2    | resident rows: `.jl`, XML-in-prompts/JSON-in-IPC, `submit_turn`, `:8080` everywhere, dynamic-vs-static in one sentence. The `L45` narrative → `5/28`; "adding a channel adapter" → `EXTENDING.md`                                                                                                |
| Trust boundaries                      | 8 / 358   | move                   | —    | `SECURITY.md` §"Identity header trust" owns it                                                                                                                                                                                                                                                   |
| Split write-discipline                | 6 / 198   | keep                   | 2    | a direct write to another daemon's DB works until it does not                                                                                                                                                                                                                                    |
| Subagent worktrees                    | 16 / 792  | keep 2 lines           | —    | the parallel-edit ban is in `~/.claude/CLAUDE.md` word for word; resident: the >5-files worktree threshold                                                                                                                                                                                       |
| Design principles                     | 13 / 562  | move                   | —    | `ARCHITECTURE.md`; not needed before acting                                                                                                                                                                                                                                                      |
| Data Dir                              | 15 / 700  | move                   | —    | `ARCHITECTURE.md:996` mount table already maps `<data>/groups/<folder>/`                                                                                                                                                                                                                         |
| Config                                | 16 / 768  | keep 1 line, move      | 2    | resident: `APP_SRC_DEV` never on an instance (`M1`). The var list → `INSTALL.md`                                                                                                                                                                                                                 |
| Entrypoint                            | 12 / 453  | move                   | —    | it says "full list in `cmd/arizuko/README.md`"                                                                                                                                                                                                                                                   |
| Quick Setup                           | 14 / 520  | keep 1 line, move      | 2    | resident: deploy = `systemctl restart`, `arizuko run` is setup only. The rest is `INSTALL.md` by its own first sentence                                                                                                                                                                          |
| Service Architecture                  | 7 / 246   | merge                  | —    | a row                                                                                                                                                                                                                                                                                            |
| Operational check (five gates)        | 61 / 3048 | move, 2-line rule      | —    | `.claude/skills/eval/SKILL.md` exists for exactly this and gate 4 already invokes it. Resident: a deploy is not done until `anteval --smoke` passes, and the reason (`L149` shipped green through gates 1–4)                                                                                     |
| OTLP export                           | 10 / 520  | move                   | —    | `ARCHITECTURE.md:849`                                                                                                                                                                                                                                                                            |
| Shipping changes                      | 14 / 761  | move, 1-line rule      | 2    | `ant/skills/self/migrations/CLAUDE.md` owns the steps. Resident: every release bumps `MIGRATION_VERSION`, docs-only included — forgetting it is silent                                                                                                                                           |
| Tagging a new version                 | 16 / 947  | move                   | —    | the same project skill as Refine scope's neighbour: `.claude/skills/release/` (open question 5)                                                                                                                                                                                                  |
| Announcing                            | 20 / 533  | move                   | —    | `CHANGELOG.md`'s head, where the shape is used                                                                                                                                                                                                                                                   |
| Deploy policy                         | 6 / 210   | keep                   | 2    | deploying to a second instance unasked raises no error                                                                                                                                                                                                                                           |
| "Nothing works" checklist             | 13 / 708  | move                   | —    | `.claude/skills/eval/` method; `INSTALL.md:305` already has exit 125                                                                                                                                                                                                                             |
| Migrating from kanipi                 | 3 / 46    | merge                  | —    | a row                                                                                                                                                                                                                                                                                            |

Estimate: ≈7 KB resident from 28.7 KB. The rule lines survive in full;
what leaves is narrative that a spec, a bug entry or a directory-scoped
file already records.

## The carve-out, argued

The maintainer's map thesis holds for a generic project because the
reader arrives with a question. An arizuko agent arrives with a payload,
and each item below is something it must handle correctly before it has
formed any question — so a pointer to a skill it does not know exists
cannot help. What breaks if each stays out:

- **The envelope** (`<messages>`, `<reply-to>`, `<attachment>`,
  `<autocalls>`, `<topic>`, `<observed>`, `<link-context>`). Out: the agent
  treats XML as user text, replies to observed context, re-fetches what
  autocalls carry, answers the last thing it said instead of the message
  the user pointed at. No error, and no user phrase triggers a skill.
  `6/7` makes the envelope the agent contract for this reason.
- **Delivery** (only the final text block leaves the container; a
  `<status>` owes an answer; `<think>`-only is silence; a mention must
  produce visible output). Out: answers destroyed with zero error —
  `L149` measured exactly this, and `L150` records that nothing downstream
  reports it. Silent by construction until the mechanism half of `L149`
  ships; then these lines leave under rule 2.
- **Authority** (a folder isolates and confers nothing; the tool list is
  the grant set; groups are created explicitly). Out: the model's prior is
  hierarchical, and `5/33` removed the tiers that prior expects. The agent
  reasons from depth, asks for a `chown` on a missing `web:publish` grant
  (the file records four such requests), and works around a missing tool.
- **The register floor** (terse, the ceiling, no marketing adjectives,
  STE). Out: replies balloon and nothing objects. `4/P` decided the floor
  lives here because one output style loads per session and personas add
  voice without lowering it.
- **The map itself** (`/self` for platform questions, the memory-store
  rows, connector tools deferred behind Tool Search, the storage table).
  Out: no skill is ever loaded — the model defaults to raw capability
  unless an instruction names the concrete action, and a tool it cannot
  see is a tool it declares missing.
- **Rigor.** Out: a fabricated number carries no error.

Everything else in the file is reachable by name once these are in hand,
or produces a visible error when ignored.

## The failure mode, and how the proposal detects it

A rule that stops being loaded stops being followed, silently. The move
above makes ≈19 KB of instruction contingent on the agent choosing to
load it, and the record says the model skips skills unless forced. Three
guards, one per class of moved content:

1. **No case, no move.** A section leaves residency only if an
   `anteval` case exercises the behaviour it teaches — `anteval` is the
   agent conformance basis (`5/9`, `6/7`), `--smoke` is gate 5 of every
   deploy, and the ≈$5 run is what turns "the agent still follows this"
   from a belief into a measurement. Of the sections marked move above,
   these are covered by the 22 cases in `anteval/cases/anteval.toml`
   today: Publishing (`priv-401`, `priv-deny`, `priv-grant`, `web-route`,
   `spawn-publish-reach`), Egress (`egress-diagnose`,
   `egress-dns-distinguish`, `self-grant-noop`), attachments
   (`read-attachment-honesty`), local paths (`link-is-public-url`), the
   map (`self-skill`), storage (`state-persist`). **Not covered**: Social
   actions (`platform_id` as target), Slack/Discord threading, the
   reaction floor, the `send_file` caption, Runtimes, gateway commands,
   Model economy. Each of those needs a case before it moves; until then
   it stays, and its stay is the measurement gap made visible.
2. **The map cannot rot.** Every pointer row in the resident file names a
   path under `ant/skills/` or an output style, and a test resolves each
   one — the same shape `tests/specrefs` applies to spec links. A row
   whose target moved fails the build instead of routing the agent to
   nothing.
3. **Root moves explanations only.** Every invariant keeps its sentence
   resident; the maintainer file has no harness, so nothing that is a
   rule stops loading. What stops loading is the story of why, and the
   story lives where it is checked: the spec, the bug entry, the diary.

What this does not detect: a claude agent on a group whose operator has
edited `~/.claude/CLAUDE.md` (three live sizes on krons say at least two
have) merges the restructure through `.merge-base`; a conflict there is
the operator's to resolve, and `/migrate` reports it.

## The strongest objection

Prompt caching makes a stable prefix cheap to re-read, and attention
dilution — not token cost — is the only cost that survives caching. The
proposal converts a cached, always-present read into an on-demand read
the model performs some fraction of the time, to save cache-read tokens
that are already discounted. If the maintainer's aim is cost, the tool
is measurement (the per-model usage rows of `11/19` carry cache-read
counts per turn) and the answer may be "leave it". If the aim is
attention — the rule in the middle of a 388-line file being the one
skipped — the proposal stands, and `L130` adds a second reason: a codex
or pi agent receives none of this file today, and the proposal there is
to prepend it per turn, which prices every resident byte three times.

## Open questions

1. **Subject.** The brief names the root file; the container loads
   `ant/CLAUDE.md`. This spec applies one rule to both. Confirm both are in
   scope, or cut one.
2. **A stale claim in the resident file.** `ant/CLAUDE.md` §Persona layer
   1 says the full `PERSONA.md` body is folded into the system prompt at
   session start. `container/runner.go:531` reads it into `in.Persona`;
   `ant/src/index.ts` `ContainerInput` carries `systemMd` and no `persona`
   field, and `buildSystemPrompt` renders `SYSTEM.md` + the output style
   only. Either the sentence is wrong or a reader is missing. Not filed
   in `BUGS.md` by this spec's constraints.
3. **`/resolve` is resident.** The nudge invokes it every turn, so its
   70-line body and its bash loop over 93+ skill descriptions run every
   turn beside Claude Code's own preloaded listing of the same
   descriptions. `8/A` is the spec for the catalog cost; does this spec's
   budget count `/resolve`, and does `8/A` fold it?
4. **Order against `L130`.** The codex/pi proposal prepends the manual
   per turn. A 15 KB manual is a different decision from a 34 KB one;
   which is signed off first?
5. **Project skills that shadow global names.** `refine` and `release`
   exist in `~/.claude/skills/`; a repo-local `.claude/skills/refine/`
   carrying arizuko's refine scope shadows or merges with it depending on
   Claude Code's precedence. Accept the shadowing, or name them
   `arizuko-refine` / `arizuko-release`?
6. **The STE block.** Nine of Response Style's 21 lines are the STE
   rules. `4/P` locks the register in the resident file; is the STE
   detail part of "the register", or does it move beside the caveman
   authority the way the maintainer's own `~/.claude` keeps it?
7. **Product `~/CLAUDE.md`.** On four krons groups the product file is
   larger than the platform file (up to 43.7 KB). `5/28` owns it; this
   spec does not touch it. Should the same rule bind product authors?
8. **Tool descriptions as owners.** Moving Social actions into
   `ipc/ipc.go` descriptions puts behaviour rules in Go string literals
   that load eagerly anyway; moving them to `channels` puts them behind a
   skill the model must choose. Which is the declaration?

## Pointers

- `ant/CLAUDE.md` — the resident file for agents.
- `CLAUDE.md` — the resident file for maintainers.
- `container/runner.go:1246` — seeds the agent file (seed-once + merge-base).
- `container/runner.go:531` — `SYSTEM.md`, `in.Persona`, the `[resolve]` nudge.
- `ant/src/index.ts:47` — `buildSystemPrompt`; `ant/src/claude.ts:463,473` — preset and `settingSources`.
- `routd/prompt.go:35` — the envelope renderer.
- `container/blend.go:441` — product `CLAUDE.md` regions (`5/28`).
- `ant/skills/self/SKILL.md` — the dispatch table that is the map.
- `anteval/cases/anteval.toml` — the 22 conformance cases; guard 1's ledger.
- `specs/CLAUDE.md`, `template/web/CLAUDE.md`,
  `ant/skills/self/migrations/CLAUDE.md` — directory-scoped files, the
  repo's on-demand precedent for the root file.
- `BUGS.md` `L130`, `L149`, `L150`, `L155` — the failures this spec cites.
