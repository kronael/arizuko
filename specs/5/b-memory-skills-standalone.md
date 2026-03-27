---
status: draft
---

# Memory Skills — Standalone Distribution Spec

## Executive Summary

Arizuko's memory system is a set of SKILL.md files plus two lightweight Go
packages that live in the container. The skills themselves are plain Markdown
— they carry zero runtime dependencies. The gateway machinery (context
injection, MCP tools, get_history) is arizuko-specific and cannot be extracted
without replacement. This spec documents every layer, separates what is
portable from what is not, and defines a minimal distribution format for
adopters who want the memory layer without the full router.

---

## 1. What the Memory Skills Do

### 1.1 Skill map

| Skill              | Files written                                                                                                         | Files read                                  | Trigger                                         |
| ------------------ | --------------------------------------------------------------------------------------------------------------------- | ------------------------------------------- | ----------------------------------------------- |
| `diary`            | `~/diary/YYYYMMDD.md`                                                                                                 | —                                           | Agent decides anything notable happened         |
| `facts`            | `~/facts/*.md`                                                                                                        | `facts/`                                    | Unknown topic; `/recall-memories` found nothing |
| `recall-memories`  | —                                                                                                                     | `facts/`, `diary/`, `users/`, `episodes/`   | Technical question, person lookup, recent work  |
| `recall-messages`  | `~/tmp/messages.json` (ephemeral)                                                                                     | messages.json                               | "what did X say", past conversation             |
| `compact-memories` | `~/episodes/YYYYMMDD.md`, `~/episodes/2026-W*.md`, `~/episodes/2026-03.md`, `~/diary/week/*.md`, `~/diary/month/*.md` | session JSONL, diary entries, episode files | Scheduled cron or manual invocation             |
| `users`            | `~/users/<id>.md`                                                                                                     | `~/users/<id>.md`                           | Learning something durable about a person       |

### 1.2 Skill descriptions

**`diary`** — The primary persistent memory mechanism. One file per day
(`diary/YYYYMMDD.md`) with YAML frontmatter containing a `summary:` block and
timestamped entries (`## HH:MM`). The `summary:` value is extracted by the Go
`diary` package and injected into every container prompt as a `<knowledge
layer="diary">` XML block. Entries are prose-style notes under 250 chars each.
The agent rewrites the summary on every new entry, keeping it to ~5 bullet
points of what matters most for cold-start context.

**`facts`** — Researched, verified knowledge. NEVER written by hand — only via
this skill, which spawns a research subagent then a verifier subagent. Files
live in `facts/` with YAML frontmatter: `topic`, `category`, `verified_at`,
`summary` (one sentence, used by `/recall-memories` for fast grep), plus full
content below. Files older than 14 days are considered stale and refreshed on
next `/facts <topic>` call.

**`recall-memories`** — Read-only search across all memory stores. Has two
protocol versions: v1 (grep-based: scans `summary:` frontmatter in all `.md`
files under `facts/`, `diary/`, `users/`, `episodes/`) and v2 (CLI-based:
runs the `recall` binary with each term, then spawns an Explore subagent to
judge relevance). Falls back to v1 if the `recall` binary is absent. Explicitly
never writes files.

**`recall-messages`** — Searches chat message history. Arizuko-specific:
calls the `get_history` IPC action to fetch raw messages from the gateway
store, writes them to `~/tmp/messages.json`, then spawns an Explore subagent
to grep. This skill has no standalone equivalent without a message store.

**`compact-memories`** — Progressive compression pipeline. Two stores with
different hierarchies:

- Episodes: `day` (JSONL transcripts → `episodes/YYYYMMDD.md`), `week`
  (7 daily files → `episodes/2026-W11.md`), `month` (weekly files →
  `episodes/2026-03.md`).
- Diary: `week` (`diary/YYYYMMDD.md` → `diary/week/2026-W11.md`), `month`
  (weekly → `diary/month/2026-03.md`).

Output files have a `summary:` frontmatter bullet list (used by
`/recall-memories`) and a `sources:` list back to the level below. Intended to
run on cron via `schedule_task` MCP tool.

**`users`** — Per-user memory. Files named by platform + ID (`tg-123456.md`,
`wa-5551234.md`). YAML frontmatter: `name`, `first_seen`, `summary`. Profile
section (stable: role, expertise, preferences) + Recent section (interaction
log, ~50 lines max, oldest dropped). When the gateway injects `<user
id="..." name="..." memory="~/users/tg-123456.md" />`, the agent reads that
file for context. The `summary:` value is indexed by `/recall-memories`.

---

## 2. Current Architecture — How Skills Are Injected

### 2.1 Seeding (one-time, first container spawn)

`container.seedSkills()` in `container/runner.go` (lines 690–738):

1. Reads `container/skills/` on the host (where arizuko source is mounted as
   `/workspace/self` inside the container).
2. Copies each skill directory to `groups/<folder>/.claude/skills/`
   if the destination does not exist. This is the `~/.claude/skills/` path
   inside the container.
3. Copies `ant/CLAUDE.md` → `groups/<folder>/.claude/CLAUDE.md`
   if the destination does not exist.
4. Writes a synthetic `.claude.json` (firstStartTime, userID derived from
   folder name, migration flags).

Seeding is one-time and non-destructive. The agent can edit its own skills
after seeding. Updates propagate via the `/migrate` skill.

### 2.2 Prompt-time context injection (every container run)

`container.Run()` in `container/runner.go` (lines 87–136) builds `Annotations`
before the user prompt. In order:

1. **Migration warning** — if agent's `MIGRATION_VERSION` < host's latest,
   prepends a `[pending migration]` annotation.
2. **Episodes** — `container.ReadRecentEpisodes(groupDir)` reads `episodes/`
   from the group folder, extracts `summary:` and `type:` from each `.md`
   file's YAML frontmatter, returns up to 3 entries per type level as
   `<episodes count="N"><entry key="..." type="...">summary</entry></episodes>`.
3. **Diary** — `diary.Read(groupDir, 14)` reads up to 14 most-recent diary
   files, extracts `summary:` from each, emits
   `<knowledge layer="diary" count="N"><entry key="YYYYMMDD" age="today|yesterday|N days ago">summary</entry></knowledge>`.
4. **User context** — `router.UserContextXml(sender, groupDir)` reads
   `users/<id>.md` for the `name:` field, emits `<user id="..." name="..."
memory="~/users/id.md" />`. If no user file exists, emits just the id attr.

All annotations are prepended to the user's prompt as raw text.

### 2.3 Skills in Claude Code session

The `agent-runner/src/index.ts` sets `settingSources: ['project', 'user']` in
the SDK `query()` call. Claude Code reads `~/.claude/CLAUDE.md` and
`~/.claude/skills/*/SKILL.md` automatically at session start. Skills appear
as slash commands via the SDK's `Skill` tool.

The CLAUDE.md (seeded from `container/CLAUDE.md`) instructs the agent:

- On new sessions: read 2 most recent diary files, then read the most recent
  `.jsonl` transcript injected by the gateway as `<previous_session id="...">`.
- Use the right memory store: `diary` for events, `users` for people, `/recall-
memories` + `/facts` for knowledge.
- Never claim "no context" without searching first.

### 2.4 PreCompact hook (conversation archiving)

`createPreCompactHook()` in `agent-runner/src/index.ts` (lines 163–208) fires
before Claude Code compacts its context window. It:

1. Parses the session `.jsonl` transcript.
2. Writes a Markdown summary to `~/conversations/YYYY-MM-DD-<slug>.md`.
3. Returns a `systemMessage` that tells the agent to update today's diary
   entry before compaction happens.

This ensures diary continuity even when the context window fills.

### 2.5 Migration system

`container/skills/self/migrations/NNN-description.md` files define incremental
updates to the skill set. The `/migrate` skill (root group only) reads
`MIGRATION_VERSION` in each group's `~/.claude/skills/self/`, runs all pending
migrations in order, and updates the version file. Latest canonical version is
tracked in `container/skills/self/MIGRATION_VERSION` (currently `48`).

---

## 3. MCP Tools Inventory

The `ipc` package (`ipc/ipc.go`) serves a per-container MCP server over a
Unix socket (`/workspace/ipc/router.sock`), bridged via `socat` as the
`arizuko` MCP server in the SDK config.

### 3.1 Tools table

| Tool             | Memory-related? | Notes                                          |
| ---------------- | --------------- | ---------------------------------------------- |
| `send_message`   | No              | Send text to a chat JID                        |
| `send_reply`     | No              | Reply to a chat, auto-threads                  |
| `send_file`      | No              | Send file from `~/` to chat                    |
| `reset_session`  | Indirectly      | Clears session ID; diary/facts persist on disk |
| `inject_message` | No              | Insert message into store                      |
| `register_group` | No              | Create new group                               |
| `escalate_group` | No              | Forward to parent group                        |
| `delegate_group` | No              | Forward to child group                         |
| `refresh_groups` | No              | Reload group metadata                          |
| `get_routes`     | No              | Read routing table                             |
| `set_routes`     | No              | Replace routing table                          |
| `add_route`      | No              | Add a routing rule                             |
| `delete_route`   | No              | Remove a routing rule                          |
| `schedule_task`  | Memory-adjacent | Drives `/compact-memories` cron jobs           |
| `pause_task`     | Memory-adjacent | Pause compaction schedules                     |
| `resume_task`    | Memory-adjacent | Resume compaction schedules                    |
| `cancel_task`    | Memory-adjacent | Delete compaction schedules                    |
| `list_tasks`     | Memory-adjacent | View scheduled compaction tasks                |
| `get_grants`     | No              | ACL management                                 |
| `set_grants`     | No              | ACL management                                 |

None of the MCP tools are directly memory read/write — memory is handled by
the agent via filesystem (SKILL.md protocols) and the `diary`/`episodes`
packages that inject summaries at prompt-time. `schedule_task` is the only
tool that an agent uses for memory maintenance (scheduling `/compact-memories`
crons).

### 3.2 get_history

`recall-messages/SKILL.md` references `get_history` as an IPC action
(not an MCP tool). No implementation of `get_history` was found in the
codebase — it is not in `ipc/ipc.go` and not in any Go file under
`/home/onvos/app/arizuko/`. See Open Questions.

### 3.3 recall binary (v2)

`recall-memories/SKILL.md` references a `recall` CLI binary for v2 search
(FTS5 + vector). No such binary exists in the arizuko codebase or Dockerfile.
The skill falls back to v1 (grep-based) when `which recall` returns nothing.
The migration `037` notes mention "Recall v2 does hybrid FTS5+vector search"
but no binary is present. v1 grep path is what actually runs today. See Open
Questions.

---

## 4. Standalone Distribution Design

### 4.1 What is portable

The memory system has two separable layers:

**Layer A — Prompt instructions (zero dependencies)**

The five SKILL.md files plus the CLAUDE.md memory-routing section. These are
plain Markdown that Claude Code loads automatically when placed in
`~/.claude/skills/<name>/SKILL.md`. They work wherever Claude Code (the CLI)
runs. No arizuko, no docker, no gateway needed.

Portable skills:

- `diary/SKILL.md`
- `facts/SKILL.md`
- `recall-memories/SKILL.md`
- `compact-memories/SKILL.md` (episodes store depends on session transcript
  path convention — see section 4.4)
- `users/SKILL.md`

Not portable without replacement:

- `recall-messages/SKILL.md` — requires `get_history` IPC action
- MCP tools for scheduling — requires arizuko gateway

**Layer B — Gateway context injection (requires replacement)**

The Go packages `diary` and `container.ReadRecentEpisodes` inject summaries at
prompt time by reading frontmatter from disk files. This is an optimization —
the agent reads the same information itself on session start per `self/SKILL.md`
and `container/CLAUDE.md` instructions. The gateway injection is a shortcut
that avoids making the agent spend turns reading files. Without arizuko, the
agent must do this itself (and the CLAUDE.md instructs it to).

### 4.2 Minimal footprint — just CLAUDE.md snippets

The smallest adoptable unit is a CLAUDE.md section that covers memory routing
rules. This is already extractable from `container/CLAUDE.md`:

```markdown
# Memory stores

Use the right store — never write directly to `facts/`:

- **Something happened / was decided** → `/diary` skill
- **Learned something about a user** → `/users` skill
- **Need researched knowledge on a topic** → `/recall-memories <topic>` first;
  if no match, `/facts <topic>` to research and write verified facts
- **Facts are stale** (`verified_at` older than 14 days) → `/facts <topic>` to refresh

Never write `facts/*.md` files by hand.
```

Plus the session continuity section:

```markdown
# Session Continuity

On every NEW session, recover context from past sessions:

1. Read the 2 most recent `diary/*.md` files (by filename date)
2. If diary is empty, find past session files:
   ls -t ~/.claude/projects/<project-slug>/\*.jsonl | head -3
   Read the most recent (tail last 100 lines)

NEVER say "I don't have context" without FIRST searching diary/ and session
transcripts.
```

These two sections plus the five portable skill files constitute the complete
memory system for standalone use.

### 4.3 How someone uses this WITHOUT arizuko

**Requirements:**

- Claude Code CLI installed (`npm install -g @anthropic-ai/claude-code`)
- A working directory where the agent will run (the "group folder")

**Setup:**

```bash
# Pick a project directory
mkdir -p ~/my-agent
cd ~/my-agent

# Place skills
mkdir -p ~/.claude/skills/{diary,facts,recall-memories,compact-memories,users}

# Copy SKILL.md files from this repo:
# container/skills/diary/SKILL.md         → ~/.claude/skills/diary/SKILL.md
# container/skills/facts/SKILL.md         → ~/.claude/skills/facts/SKILL.md
# container/skills/recall-memories/SKILL.md → ~/.claude/skills/recall-memories/SKILL.md
# container/skills/compact-memories/SKILL.md → ~/.claude/skills/compact-memories/SKILL.md
# container/skills/users/SKILL.md         → ~/.claude/skills/users/SKILL.md

# Add memory routing to CLAUDE.md
# Copy the "Memory stores" and "Session Continuity" sections from
# container/CLAUDE.md into ~/.claude/CLAUDE.md (or project CLAUDE.md)

# Run Claude Code
claude --dangerously-skip-permissions
```

**What works:**

- `/diary` — agent writes `diary/YYYYMMDD.md` in the cwd
- `/facts <topic>` — agent researches and writes `facts/<topic>.md`
- `/recall-memories <question>` — agent greps `summary:` fields in all memory stores
- `/users` — agent reads/writes `users/<id>.md`
- `/compact-memories episodes day` — reads `~/.claude/projects/<slug>/*.jsonl`,
  but the path convention is different standalone (see 4.4)

**What does NOT work standalone:**

- `/recall-messages` — no `get_history` IPC. The skill gracefully says "not
  supported in this environment."
- `schedule_task` for cron compaction — no scheduler daemon. User invokes
  `/compact-memories` manually or sets up their own cron.
- Gateway prompt injection (diary/episodes summaries prepended automatically)
  — agent must read diary itself on session start. The CLAUDE.md instructions
  cover this, so it works, just costs one extra turn.

### 4.4 compact-memories path convention

The `compact-memories` skill reads session transcripts from
`.claude/projects/-home-node/*.jsonl`. This path is container-specific: in
arizuko the group folder is mounted as `/home/node`, so Claude Code's project
slug is `-home-node`.

Standalone usage: the project slug depends on the actual cwd path. For example,
if running from `/home/alice/my-agent`, the slug would be `-home-alice-my-agent`
and JSONL files live at `~/.claude/projects/-home-alice-my-agent/*.jsonl`.

**Open question** (see section 5): the SKILL.md hardcodes `-home-node`. For
standalone distribution, this needs to be either parameterized or removed
from the skill with a note that users must adapt the path.

### 4.5 How someone uses this WITH a different system

For a non-arizuko system that wants the gateway-injection benefit, implement:

1. **Diary injection** — before delivering user message to Claude Code, read
   `<group-dir>/diary/` for up to N recent files, extract `summary:` from each
   file's YAML frontmatter, wrap in `<knowledge layer="diary">...</knowledge>`,
   prepend to prompt. Corresponds to `diary.Read()` in `diary/diary.go`.

2. **Episodes injection** — read `<group-dir>/episodes/` for `*.md` files,
   extract `summary:` and `type:` from YAML frontmatter, wrap in
   `<episodes>...</episodes>`, prepend to prompt. Corresponds to
   `ReadRecentEpisodes()` in `container/episodes.go`.

3. **User context injection** — for a known sender ID, check
   `<group-dir>/users/<id>.md` for `name:` field, emit `<user id="..." name="..."
memory="~/users/id.md" />`, prepend to prompt. Corresponds to
   `router.UserContextXml()` in `router/router.go`.

These three functions are entirely self-contained relative to the memory file
layout — they only read from disk, no database, no IPC. They can be ported to
any language in ~100 lines each.

### 4.6 Distribution format options

**Option A — Git-clonable snippet directory (recommended)**

A single public repo with only the portable files:

```
memory-skills/
  skills/
    diary/SKILL.md
    facts/SKILL.md
    recall-memories/SKILL.md
    compact-memories/SKILL.md    # with path note
    users/SKILL.md
  CLAUDE-memory-section.md       # paste into project CLAUDE.md
  README.md
  inject/
    diary.go                     # diary.Read() extracted
    episodes.go                  # ReadRecentEpisodes() extracted
    router.go                    # UserContextXml() extracted
```

Users: `git clone`, copy skills to `~/.claude/skills/`, copy CLAUDE snippet.
No npm, no build step. Works with Claude Code CLI out of the box.

**Option B — npm package (heavier)**

Wrap the Go injection helpers in a Node.js port, publish to npm. Useful for
TS-based systems. More ongoing maintenance. Probably premature until there are
multiple adopters.

**Option C — Embedded in agent-runner (current state)**

Not portable. Requires the full arizuko stack. Documents how it works but
cannot be adopted independently.

Option A is the right call for a first distribution.

---

## 5. Open Questions

**OQ-1: `get_history` IPC action is not implemented.**
`recall-messages/SKILL.md` calls `get_history` as a JSON IPC action to the
gateway. No implementation exists in `ipc/ipc.go` or any other Go file.
Either it was removed, planned but not built, or exists in an older branch.
The skill handles the failure gracefully ("not supported in this environment"),
so it does not break anything, but `/recall-messages` cannot actually fetch
history. Before distributing, clarify: does `get_history` need to be
implemented in the gateway? Or should `recall-messages` be documented as
arizuko-only / future?

**OQ-2: `recall` binary (v2) does not exist.**
Migration `037` and the `recall-memories` skill reference a `recall` CLI that
does FTS5 + vector search. The binary is not in the Dockerfile, not in the
repo, and `which recall` inside any container would fail. The skill falls back
to v1 grep, which works. Questions: is the `recall` binary planned to be
added to the container image? Is it a separate project? Should the v2 code
path be removed from the skill until the binary exists?

**OQ-3: `compact-memories` hardcodes the container transcript path.**
The skill reads `.claude/projects/-home-node/*.jsonl`. This only works inside
an arizuko container where the group folder is `/home/node`. For standalone
distribution, this path needs to be either dynamically resolved (via `echo
~/.claude/projects/$(basename $(pwd))/*.jsonl` substitution) or documented as
"must be adapted." A portable version should use `$(ls -t ~/.claude/projects/*/
*.jsonl 2>/dev/null | head -1 | xargs dirname)` to find the right project dir.

**OQ-4: `schedule_task` parameter mismatch.**
The `compact-memories` skill calls `schedule_task({ targetFolder: ..., prompt:
..., schedule_type: ..., schedule_value: ..., context_mode: ... })` but the
MCP server implementation in `ipc/ipc.go` takes `targetJid`, `prompt`, `cron`,
`contextMode`. The skill uses `targetFolder`/`schedule_type`/`schedule_value`
which do not match the MCP parameters. Either the skill was written against an
older API or there is a translation layer. Needs verification against a running
instance.

**OQ-5: AGPL licensing scope for extracted skills.**
Arizuko is AGPL v3 (per `.diary/20260323.md`). Extracting SKILL.md files (which
are pure prompt text, not code) into a separate public repo raises the question
of whether AGPL applies to them. AGPL is a software license; prompt text is
arguably not "software" in the FSF sense. Needs a licensing decision before
public distribution: either relicense the skills as CC0/MIT or carry forward
AGPL with the understanding that adopters using them in a network service must
share modifications.

**OQ-6: `episodes/` path inside container vs standalone.**
`compact-memories` for `episodes day` reads
`.claude/projects/-home-node/*.jsonl` which is the SDK's session transcript
dir when cwd is `/home/node`. Standalone users run from a different cwd, so
this path is always wrong for them. Related to OQ-3 but distinct: episode
day-level compaction needs the raw JSONL, not just diary files.

---

## Appendix A — Memory Store Layout

```
~/                          (group folder, mounted as /home/node in container)
├── diary/
│   ├── YYYYMMDD.md         daily log — YAML frontmatter with summary:
│   ├── week/
│   │   └── 2026-W11.md     weekly diary rollup from compact-memories
│   └── month/
│       └── 2026-03.md      monthly diary rollup from compact-memories
├── facts/
│   └── <topic-slug>.md     researched facts — topic, category, verified_at, summary:
├── users/
│   └── <platform-id>.md    per-user memory — name, first_seen, summary:, Recent
├── episodes/
│   ├── YYYYMMDD.md         day-level episode from session JSONL
│   ├── 2026-W11.md         week-level from daily episodes
│   └── 2026-03.md          month-level from weekly episodes
├── conversations/          archived pre-compact transcripts (Markdown)
└── .claude/
    ├── CLAUDE.md           agent instructions (seeded once from container/CLAUDE.md)
    ├── skills/
    │   ├── diary/SKILL.md
    │   ├── facts/SKILL.md
    │   ├── recall-memories/SKILL.md
    │   ├── recall-messages/SKILL.md
    │   ├── compact-memories/SKILL.md
    │   ├── users/SKILL.md
    │   └── self/
    │       ├── SKILL.md
    │       ├── MIGRATION_VERSION
    │       └── migrations/
    └── projects/
        └── -home-node/     SDK session state (JSONL transcripts, memory/)
```

## Appendix B — Injection Data Flow

```
Before each container run (gateway side):

  groupDir/diary/YYYYMMDD.md    → diary.Read()            → <knowledge layer="diary">
  groupDir/episodes/*.md        → ReadRecentEpisodes()    → <episodes>
  groupDir/users/<id>.md        → UserContextXml()        → <user id="..." memory="..."/>
  SOUL.md (optional)            → readOptional()          → prepended to systemPrompt
  SYSTEM.md (optional)          → readOptional()          → prepended to systemPrompt

  Annotations prepended to user prompt in order listed above.

On session start (agent side, per container/CLAUDE.md):

  1. Read 2 most recent diary/*.md
  2. If diary empty: read tail of most recent ~/.claude/projects/<slug>/*.jsonl
  3. Check for <previous_session id="..."> injected by gateway
```

## Appendix C — Seeding Code Path

```
container.Run()
  └── seedSkills(cfg, sessDir, folder)     (runner.go:690)
        ├── for each dir in container/skills/:
        │     if ~/.claude/skills/<name>/ missing: cpDir()  [one-time]
        ├── if ~/.claude/CLAUDE.md missing: copy container/CLAUDE.md  [one-time]
        └── if ~/.claude/.claude.json missing: write synthetic JSON
```

The `cpDir()` function is a simple recursive copy — no diff, no merge. Skills
can diverge after seeding. The `/migrate` skill handles upgrades intelligently.
