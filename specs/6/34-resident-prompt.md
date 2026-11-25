---
status: partial
defects: [L188]
depends: [../8/A-hierarchical-skills]
---

# specs/6/34 — the resident budget: what an always-loaded file may carry

The resident manual, on-demand pointers and static carve-out checks exist in
`ant/CLAUDE.md`, `ant/skills/self/` and `tests/resident/resident_test.go`.
BUGS `L188` lists the moved rules with no anteval case.

## Remaining

- Run the full case catalog, `unknown-addressee` included, through the disposable
  lifecycle (`anteval/cmd/anteval/lifecycle_test.go` holds only fakes).

Seventeen of the eighteen rules in BUGS `L188` have no anteval check kind that can observe
them: sixteen are "not externally observable with current check kinds" and one is out of
anteval scope. The owner accepts all seventeen as unmeasured (2026-11-15), so guard 1 below
does not apply to them. The `~/CLAUDE.md` mount procedure (the "Additional mounts" row of
BUGS `L188`, `EXTENDING.md`) is operator procedure and out of anteval scope.

## Problem

Turns mount the selected package manuals and skills.

Two files load without being asked for, on every turn, for a reader who
never chose them:

- `ant/CLAUDE.md` — 232 lines, 20,587 bytes, 24 sections. Seeded into
  every group as `~/.claude/CLAUDE.md`, which
  Claude Code reads on every turn because `settingSources` is
  `['project', 'user']` (`ant/src/claude.ts:473`) and the turn's cwd is
  the home (`ant/src/index.ts:243`).
- `CLAUDE.md` at the repo root — 348 lines, 19,247 bytes, 16 sections.
  Read by every maintainer session and every subagent opened in the repo.

The root file's counts were measured on 2026-11-15 at
`5ce1066d76ee58d490282b665feaec2542ed3947`: `wc -l -c CLAUDE.md`, and the top-level
headings outside code fences in that file.
The `ant/CLAUDE.md` counts, the case count and the `ant/skills/self/` figures were measured
on 2026-11-15 at `8e319b2237d535e0e9eba2bbdbf9d63b3b03079c`: `wc -l -c ant/CLAUDE.md`;
top-level headings outside code fences in that file; `grep -c '^\[\[case\]\]'
anteval/cases/anteval.toml`; `find ant/skills/self -type f` piped to `wc -l`, and `cat` of
those files piped to `wc -c`.

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
| `~/CLAUDE.md` (product regions, `EXTENDING.md`)               | 0 on five groups; 31,134–43,734 on four | each product's `CLAUDE.md` was concatenated                                                                    |
| skill listing (name + description + when_to_use, ≤1,536 each) | ≈30,800 for the 93 stock skills         | Claude Code preloads every `~/.claude/skills/*/SKILL.md` frontmatter; live groups hold 84–116 skill dirs       |
| MCP tool catalog                                              | 60 tools, ≥5,277 of description text    | `alwaysLoad: true` on the arizuko server (`8/A`); the live JSON schemas add parameter text                     |
| output style                                                  | 952–1,492                               | `ant/src/index.ts:47` `buildSystemPrompt` = `SYSTEM.md` + the selected style                                   |
| `/resolve` body                                               | ≈2,700                                  | the `[resolve]` nudge invokes it on every prompt, so it is resident in practice                                |
| envelope                                                      | variable                                | `routd/prompt.go:35`: system messages, `<autocalls>`, `<persona>` summary, `<observed>`, `<topic>`, messages   |
| memory layers                                                 | variable                                | `ant/hooks/prompt_context.py`: `work.md`, the sender's `users/<id>.md`                                         |

Fixed text before the first user word is therefore ≈71–115 KB, and the
platform file is 30–48 % of it. Tokens are not measured on this host (no
tokenizer installed); bytes ÷ 4 is the working estimate, so the platform
file alone is ≈8,500 tokens per turn.

A maintainer session has the same shape: root `CLAUDE.md` 19.2 KB +
`~/.claude/CLAUDE.md` 14.6 KB + `MEMORY.md` 23.3 KB + the listing of 86
global and 2 project skills.

### The second copy is already there

`ant/skills/self/` is a 15-file, 50,068-byte directory whose `SKILL.md`
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
they drift. The root file has the same shape: `specs/CLAUDE.md` opens
with "the settled rules from root `CLAUDE.md` — repeated here as a
checklist", `template/web/CLAUDE.md` owns "Deploy to live krons" while
the root carries 41 lines on the same subject, and
the migration instructions owned the `MIGRATION_VERSION` steps
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
4. **A workflow skill reads it from here.** A skill that names the
   project's `CLAUDE.md` as its override source makes the section a
   declaration with a named reader, and a copy at any other address is the
   second format "one declaration, many readers" forbids. Test: does a
   skill body say it reads this file? A project skill cannot take the
   section instead, because a project skill whose name matches a global one
   is shadowed by it: `create-eval` exists in both `.claude/skills/` and
   `~/.claude/skills/`, and only the global description reaches the
   session's skill listing.

Everything else is one of: a skill body (its frontmatter is the pointer),
a tool description (`tools/list` is the reader), an output style (one loads
per session, so per-surface rules belong there — `ant/skills/self/messages.md`), or the document
that owns the subject. A table of topics → skills is not a map and does not
go in the resident file: the skill listing already is that table, and a
hand copy goes stale.

The rule applies to a new section the same way: name which of the four
tests it passes in the commit that adds it; a section that passes none is
a skill.

## `ant/CLAUDE.md`, section by section

Sizes are lines / bytes of the current file. "Keep" means resident under
the rule, with the named trim; "move" names the owner and the resident
remainder, if any.

| Section                             | L / B     | Verdict            | Rule | Destination and remainder                                                                                                                                                                                                                                                                       |
| ----------------------------------- | --------- | ------------------ | ---- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Identity                            | 5 / 168   | keep               | 1    | who/where in two env vars; the introspection recipe is `self/identity.md`                                                                                                                                                                                                                       |
| Response Style                      | 21 / 2320 | keep               | 2    | the register floor; it lives here because one output style loads per session. The STE bullet list is the only trim candidate                                                                                                                                                                    |
| Rigor                               | 4 / 406   | keep               | 2    | fabrication has no error                                                                                                                                                                                                                                                                        |
| Tenancy model                       | 12 / 1861 | keep, trim         | 1    | resident: group = isolated home, path carries zero authorization, tool list = grant set, topic in one line. The `register_group` paragraph moves to `self/mcp.md` — the tool's own error says "call this"                                                                                       |
| Autocalls                           | 4 / 269   | keep               | 1    | envelope; `self/autocalls.md` holds the long form                                                                                                                                                                                                                                               |
| How messages arrive                 | 24 / 1306 | keep               | 1    | the envelope is the contract; nothing else names `<messages>` and `<reply-to>`                                                                                                                                                                                                                  |
| When to respond                     | 18 / 1638 | keep               | 2    | silence on a mention, final-block-only delivery (`L149`), `<link-context>`: all silent. Already shaped right — it points at `chat-link.md` for the full rules                                                                                                                                   |
| Greetings                           | 4 / 70    | move               | —    | `hello`'s frontmatter already routes on "hi/hello with no specific task"; the resident line is a second copy                                                                                                                                                                                    |
| Resolve                             | 4 / 634   | keep, trim         | 3    | the nudge is the mechanism; one resident line names it. `ant/skills/solve/SKILL.md` requires reading the channel before claiming no context                                                                                                                                                     |
| Task discipline                     | 5 / 408   | keep               | 2    | `AskUserQuestion` ends the turn silent. Leaves when a PreToolUse deny rule for it ships                                                                                                                                                                                                         |
| Skills and tools                    | 6 / 642   | keep               | 3    | this is the map: `/self`, and connector tools deferred behind Tool Search — the deferral is invisible without the line                                                                                                                                                                          |
| Model economy                       | 4 / 461   | move               | —    | `sonnet` and `haiku` frontmatter route on scouting and mechanical lookups; economy is not a silent failure                                                                                                                                                                                      |
| Memory stores                       | 8 / 388   | keep               | 3    | four rows: event → skill. The `/arizuko-issue` line joins it as a fifth row                                                                                                                                                                                                                     |
| Recording user-reported issues      | 4 / 103   | move               | —    | one row in Memory stores                                                                                                                                                                                                                                                                        |
| Status updates                      | 13 / 1331 | keep, trim         | 2    | A turn that emits a status and no answer ends ✗ with nothing to read. The `TodoWrite` paragraph becomes one line; `5/24` owns the rendering                                                                                                                                                     |
| Persona                             | 9 / 834   | move               | —    | `ant/CLAUDE.md` § Response Style names the `<persona>` block and `/persona` in one sentence; `persona/SKILL.md` owns the layers                                                                                                                                                                 |
| Tool discipline                     | 4 / 506   | merge              | 2    | folds into When blocked — the same "enumerate before declaring absent" rule twice                                                                                                                                                                                                               |
| When blocked                        | 15 / 1602 | keep, trim         | 2    | resident: the three-step order. The `inspect_*`/`find_messages` paragraph is tool-description territory (`self/mcp.md`); the "check your live tool list" paragraph repeats Tenancy; the session-opening ritual is `self/SKILL.md` §"Session recovery"                                           |
| Environment (`$WEB_HOST`)           | 4 / 143   | keep               | 2    | a literal `$WEB_HOST` in chat raises no error                                                                                                                                                                                                                                                   |
| Network egress                      | 15 / 2087 | move, 3-line stub  | 2→3  | the 403 is visible, the misreading is what `egress-diagnose`, `egress-dns-distinguish` and `self-grant-noop` in `anteval/cases/anteval.toml` test. Owner: a new `self/egress.md` row in `self`'s table. Resident: default-deny; a host that 403s on every path is the allowlist; `/self` egress |
| Agent home is your kingdom          | 8 / 645   | move               | —    | `self/storage.md` holds it verbatim                                                                                                                                                                                                                                                             |
| Publishing a web page               | 39 / 3114 | move, 1-line stub  | —    | the largest section; failures are 404/401/EACCES, and `priv-401`, `priv-deny`, `priv-grant`, `web-route`, `spawn-publish-reach` test the behaviour. Owner: `web/SKILL.md` (the slots) + `self/web-routing.md`. Resident: verify the URL before you announce it                                  |
| Storage — persistent vs transient   | 16 / 903  | keep               | 3    | this table is the map (path → purpose → skill); `self/storage.md` keeps the prose. Gains one row: `/tmp` is lost at turn end                                                                                                                                                                    |
| Additional mounts (`/mnt/`)         | 14 / 889  | move               | —    | operator SQL the agent cannot run; `self/workspace.md` owns the mount table                                                                                                                                                                                                                     |
| CLAUDE.md ownership                 | 10 / 936  | move               | —    | matters only during `/migrate`; the migration instructions owned seeding and the 3-way merge                                                                                                                                                                                                    |
| Web routing and auth                | 20 / 1506 | move               | —    | `self/web-routing.md` holds the same table                                                                                                                                                                                                                                                      |
| Gateway commands                    | 11 / 368  | move               | —    | `self/commands.md`; "when asked for help, mention these" is `hello`'s third part                                                                                                                                                                                                                |
| `@<unknown>` prefix                 | 4 / 418   | move               | —    | `self/chat-routing.md`; the message reaches the agent as visible text                                                                                                                                                                                                                           |
| Runtimes                            | 8 / 460   | move               | —    | `self/runtimes.md` holds the same list; bare `pip` fails loudly                                                                                                                                                                                                                                 |
| Inbound media attachments           | 13 / 713  | keep, trim         | 1    | resident: the `<attachment>` shape and "no tag = the file did not arrive" (`read-attachment-honesty` tests it). The transcript and PDF notes move to `self/messages.md`                                                                                                                         |
| Social actions                      | 11 / 1515 | move               | —    | `reply`'s description already says "THE DEFAULT way to respond" in 674 characters the agent reads every turn; the `platform_id` and `ErrUnsupported` rules belong in the verbs' descriptions, else `channels`                                                                                   |
| Slack threading / Discord threading | 8 / 821   | move               | —    | per-surface: `slack-thread.md`, `slack-channel.md`, `discord*.md` output styles (`ant/skills/self/messages.md`)                                                                                                                                                                                 |
| Reactions                           | 8 / 1079  | keep 1 line, move  | 2    | resident: a reaction is the floor when a reply is expected (silence is silent). Register mirroring and DM anti-patterns → the `*-dm.md` styles                                                                                                                                                  |
| Telegram groups                     | 4 / 516   | move               | —    | `telegram-group.md` output style                                                                                                                                                                                                                                                                |
| Delivering files to users           | 4 / 458   | keep 1 line        | 2    | inlining a file is not an error; the caption rule belongs in `send_file`'s description                                                                                                                                                                                                          |
| Local paths vs public URLs          | 18 / 1409 | keep 2 lines, move | 2    | resident: the user cannot open container paths — translate first (`link-is-public-url` tests it). The translation table → `self/web-routing.md`                                                                                                                                                 |
| Response size + medium              | 9 / 994   | keep, trim         | 2    | exactly what `ant/skills/self/messages.md` specifies: a pointer at the style plus the surface-agnostic long-answer pattern. The example paragraph is the trim                                                                                                                                   |

Summing the "keep" column with its trims: ≈15 KB stays resident, ≈19 KB
leaves, and ≈12 KB of what leaves already exists at its destination. The
carve-out — envelope, delivery, authority, register, the map — is ≈10 KB
of the 15 and does not shrink under this rule.

## Root `CLAUDE.md`, section by section

The reader is a maintainer session. Its on-demand surfaces are the global
skills, the two project skills (`.claude/skills/eval`, `create-eval`), and
directory-scoped `CLAUDE.md` files that Claude Code loads when a file in
that directory is read — `specs/CLAUDE.md` and `template/web/CLAUDE.md` are the repo's own precedent for
on-demand rules. The root file has no conformance harness, so a change to it
moves **explanations, never rule lines**: every invariant keeps its one
sentence resident and its cost narrative lives in the spec or bug that
recorded it.

The file has 16 sections. Lines and bytes per section, measured with the same
split (each section runs from its `## ` heading to the next, headings included):

| Section                                       | L / B     |
| --------------------------------------------- | --------- |
| Identity is configured, never derived         | 4 / 466   |
| Canonical paths                               | 11 / 498  |
| Response Style                                | 7 / 291   |
| Minimality and orthogonality (non-negotiable) | 90 / 5647 |
| Build & Test                                  | 21 / 911  |
| When to read what                             | 37 / 2218 |
| Discoverability                               | 7 / 309   |
| Observability                                 | 7 / 269   |
| Bug triage records a finding, then fixes it   | 32 / 1684 |
| Refine scope                                  | 11 / 562  |
| Conventions                                   | 55 / 3545 |
| Subagent worktrees                            | 8 / 321   |
| Deploying                                     | 19 / 1075 |
| Shipping changes                              | 8 / 378   |
| Tagging a new version                         | 11 / 566  |
| Announcing                                    | 18 / 494  |

The sections add to 346 lines and 19,234 bytes; the title line and the blank line
before the first section are the other 2 lines and 13 bytes.

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
  The envelope IS the agent contract for this reason.
- **Delivery** (only the final text block leaves the container; a
  `<status>` owes an answer; `<think>`-only is silence; a mention must
  produce visible output). Out: answers destroyed with zero error —
  `L149` measured exactly this, and `L150` records that nothing downstream
  reports it. Silent by construction until the mechanism half of `L149`
  ships; then these lines leave under rule 2.
- **Authority** (a folder isolates and confers nothing; the tool list is
  the grant set; groups are created explicitly). Out: the model's prior is
  hierarchical, and `5/33` leaves no tiers for that prior to land on. The agent
  reasons from depth, asks for a `chown` on a missing `web:publish` grant
  (the file records four such requests), and works around a missing tool.
- **The register floor** (terse, the ceiling, no marketing adjectives,
  STE). Out: replies balloon and nothing objects. The floor
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

## The failure mode, and the three guards

A rule that stops being loaded stops being followed, silently. The move
above makes ≈19 KB of instruction contingent on the agent choosing to
load it, and the record says the model skips skills unless forced. Three
guards, one per class of moved content:

1. **No case, no move.** A section leaves residency only if an
   `anteval` case exercises the behaviour it teaches — `anteval` is the
   agent conformance basis (`5/9`), `--smoke` is gate 5 of every
   deploy, and the ≈$5 run is what turns "the agent still follows this"
   from a belief into a measurement. Of the sections marked move above,
   these are covered by the 24 cases in `anteval/cases/anteval.toml`
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
is measurement (the gateway's per-model usage rows carry cache-read
counts per turn, `routd/README.md`) and the answer may be "leave it". If the aim is
attention — the rule in the middle of a 232-line file being the one
skipped — the proposal stands, and `L130` adds a second reason: a codex
or pi agent receives none of this file today, and the proposal there is
to prepend it per turn, which prices every resident byte three times.

## Pointers

- `ant/CLAUDE.md` — the resident file for agents.
- `CLAUDE.md` — the resident file for maintainers.
- `runed/render.go:211` — persona and `SYSTEM.md`.
- `ant/src/index.ts:47` — `buildSystemPrompt`; `ant/src/claude.ts:463,473` — preset and `settingSources`.
- `routd/prompt.go:35` — the envelope renderer.
- `ant/skills/self/SKILL.md` — the dispatch table that is the map.
- `anteval/cases/anteval.toml` — the 24 conformance cases; guard 1's ledger.
- `specs/CLAUDE.md`, `template/web/CLAUDE.md` — directory-scoped files, the
  repo's on-demand precedent for the root file.
- `BUGS.md` `L130`, `L149`, `L150`, `L155` — the failures this spec cites.
