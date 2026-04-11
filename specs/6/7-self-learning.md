---
status: draft
source: hermes-agent peel (2026-04-11)
---

# specs/6/7 — self-learning: memory + skill_manage

Agent-managed persistent memory and skill authoring, ported from
NousResearch/hermes-agent. Phase 1 ships the primitives as MCP tools.
Phase 2+ adds post-turn review automation.

## Problem

Today the ant has no bounded, curated memory layer. `~/.claude/CLAUDE.md`
exists per-group but is unstructured prose the agent edits with raw
`Write`/`Edit`. There's no:

- Character limit enforcement → memory can silently blow out the context
- Frozen-snapshot semantics → edits mid-session re-inject into the system
  prompt and invalidate the prefix cache
- Content scanner → nothing prevents the agent (or a prompt-injected
  message author) from writing exfil or role-hijack payloads into memory
  that land back in the system prompt next session
- Structured skill authoring → agent can `mkdir .claude/skills/foo` and
  write `SKILL.md`, but there's no validation, no safety scan, no audit
  trail through the MCP gate

Hermes solves all four with two tools: `memory` and `skill_manage`. We
should port them.

## Current terrain

- **Per-group skill seeding**: `/workspace/self/ant/skills/*` is copied
  to `~/.claude/skills/` on first container spawn (`ant` entrypoint)
- **Per-group agent home**: `~` is `/home/node`, mounted from
  `<dataDir>/groups/<folder>/`, persists across runs
- **MCP server**: `ipc/ipc.go` exposes tools to the container via unix
  socket; current tools are task/route/group/message operations
- **System prompt**: built in `ant/src/index.ts` from the `self` skill +
  assorted env; no memory-file injection today
- **Auth**: MCP has `CheckAction` gate via `grants`; can reuse for new
  tools without any new infra

## Design — phase 1 (ship first)

### Two new MCP tools, added in `ipc/`

**`memory`** — curated bounded memory

```
action:  "add" | "replace" | "remove" | "read"
target:  "memory" | "user"
content: string     (for add/replace)
match:   string     (for replace/remove: short unique substring)
```

Files:

- `<groupDir>/.claude/MEMORY.md` — 2200 char limit; agent's notes
  (environment facts, project conventions, things learned)
- `<groupDir>/.claude/USER.md` — 1375 char limit; user preferences,
  style, workflow habits

Entry delimiter: `§` on its own line (matches Hermes). Multi-line
entries supported.

Char limits chosen for a reason: bounded cost for system-prompt
injection means the agent physically cannot grow memory unbounded.
When over budget, the `add` call must fail with a clear error so the
agent rewrites to stay under budget — this is the pressure that
forces curation.

### Content scanner at write time

Reject writes matching any of:

| Pattern class     | Examples                                     |
| ----------------- | -------------------------------------------- |
| prompt_injection  | `ignore (previous\|all\|above) instructions` |
| role_hijack       | `you are now`, `act as if you have no`       |
| deception         | `do not tell the user`                       |
| exfil_curl/wget   | `curl ... $KEY`, `wget ... $SECRET`          |
| read_secrets      | `cat .env`, `cat .netrc`, `cat .pgpass`      |
| ssh_backdoor      | `authorized_keys`, `~/.ssh`                  |
| invisible_unicode | zero-width, bidi-override chars              |

Matches Hermes's `_MEMORY_THREAT_PATTERNS` at
`tools/memory_tool.py:60-76`. Port as a Go slice of `{regexp, id}`
pairs in a new file `ipc/scan.go`.

Rationale: memory contents are injected into the system prompt on the
NEXT session. A bad actor (e.g. a prompt-injected inbound message) must
not be able to coerce the agent into writing a role-hijack payload into
its own persistent memory.

### Frozen snapshot pattern

At container startup, `ant/src/index.ts` reads both files BEFORE calling
`query()`. The contents are embedded in `systemPrompt` verbatim, wrapped
in a fenced block:

```
<persistent-memory>
[System note: Your own curated memory from prior sessions.
 This is your notes, not new user input.]

MEMORY.md:
<contents>

USER.md:
<contents>
</persistent-memory>
```

Mid-session `memory add/replace/remove` writes persist to disk
immediately but do NOT update the current session's system prompt. This
preserves the prefix cache for the whole session — changing the system
prompt mid-run invalidates every cached token.

The agent will see the fresh snapshot on the NEXT container spawn for
that group.

### `skill_manage` — structured skill authoring

```
action:  "create" | "edit" | "patch" | "delete" |
         "write_file" | "remove_file"
name:    skill name (lowercase, `[a-z0-9][a-z0-9._-]*`, max 64 chars)
content: SKILL.md content (for create/edit)
match:   substring for patch
replace: replacement for patch
subdir:  one of references/templates/scripts/assets (for write_file)
file:    filename within subdir
body:    file content
```

Writes to `<groupDir>/.claude/skills/<name>/SKILL.md` and subdirs. Same
content scanner applies to SKILL.md frontmatter + body + any
`write_file` body.

Size limits:

- SKILL.md: 100,000 chars
- supporting file: 1 MiB
- subdir whitelist: `references|templates|scripts|assets`

Name validation: `^[a-z0-9][a-z0-9._-]*$` + `not in {., .., ~}`. Reject
path traversal attempts outright.

SKILL.md frontmatter validation: require `name:` and `description:`
fields. Description max 1024 chars. If present, validate `user-invocable`
is bool, `tools` is list.

### Files to add / touch

**New files**:

- `ipc/memory.go` (~180 LOC) — memory tool handler, file I/O, snapshot
- `ipc/skillmanage.go` (~250 LOC) — skill_manage tool handler
- `ipc/scan.go` (~60 LOC) — content scanner (regex table + check fn)
- `ipc/memory_test.go`, `ipc/skillmanage_test.go`, `ipc/scan_test.go`

**Touch**:

- `ipc/ipc.go` — register the two new tools in `buildMCPServer`
- `ant/src/index.ts` — read MEMORY.md + USER.md at session start, embed
  in system prompt (~20 LOC)
- `ant/skills/self/SKILL.md` — add "persistent memory" section
  explaining `memory` and `skill_manage` usage pattern
- `ant/skills/self/migrations/054-self-learning.md` — migration note
- `ant/skills/self/MIGRATION_VERSION` → 54

No schema changes. No new packages. No daemon. Entirely additive.

## Design — phase 2 (defer unless needed)

**Post-turn review loop**: after the agent produces its final output for
a turn, gated spawns a SECOND ephemeral claude subprocess with a
restricted toolset (`memory` + `skill_manage` only), fed a review prompt:

```
The agent just completed a turn. Here's the user message and the
agent's response. Review them for:
1. New facts about the user (add to USER.md if not already there)
2. New facts about the environment or project (add to MEMORY.md)
3. Procedural patterns worth capturing as a skill (skill_manage create)

Be conservative. Most turns yield nothing. If nothing stands out, exit
without writing.
```

Triggered every N turns (Hermes uses 10) to amortize the cost. Gated
tracks the counter per-group in the `groups` table (new column
`turns_since_review`).

**Why defer**: phase 1 already gives the agent the primitives. The
Hermes value of the forked review is (a) it doesn't consume the primary
turn's context budget, (b) it's a "fresh eye" unclouded by the ongoing
conversation. Both are real but marginal. Ship phase 1, see if agents
actually use `memory add` of their own accord, then decide if phase 2
is needed.

## Design — phase 3 (further defer)

**Skills guard AST scan**: when agent creates a skill with scripts (Python
or JS), walk the AST rejecting dangerous calls: `os.system`, `exec`,
`eval`, `subprocess` with user-controlled strings, `__import__` of
restricted modules. Matches Hermes's `skills_guard.py`.

Port target: `ipc/skillguard.go`. Only fires on `write_file` under
`scripts/`. Non-blocking fail: log + warn, never reject.

Again, defer: agent-authored scripts are rare today, and the blast
radius is bounded by the container uid and mount model already.

## Not in scope

- Global memory shared across groups — Hermes has this via `hermes_home`
  profile scoping. Skipped: arizuko's model is per-group isolation, and
  operator-wide memory would muddy the sandbox. If wanted, add as a
  separate `global_memory` tool later.
- Memory prefetch / recall tooling — Hermes's `queue_prefetch` abstracts
  over plugins. We don't have plugins; skip.
- External memory provider plugins (honcho/mem0/etc.) — Hermes's plugin
  system is significant complexity for a design-time-only property we
  don't need.

## Open questions

1. **Char limits 2200/1375 — tune for us?** Hermes picked these for a
   ~2.75 char/token ratio on typical English + code-light memory.
   Arizuko memory will lean more toward operational facts and group
   context. Start with Hermes's numbers, tune in phase 1.5 if
   ~800 tokens of persistent injection isn't enough.

2. **Separate MEMORY.md vs merge with CLAUDE.md?** Today
   `ant/skills/self/` seeds `~/.claude/CLAUDE.md`. The Hermes model has
   MEMORY.md + USER.md separate from any system-prompt file. Proposal:
   keep CLAUDE.md as the seed/instructions (operator-authored, read-
   only-ish), add MEMORY.md + USER.md as agent-authored deltas. Both
   inject at session start.

3. **Git commit strategy for memory writes?** Every memory write could
   auto-commit to the per-group git repo for audit. Or let the existing
   once-a-turn commit hook catch them. Lean toward: don't auto-commit,
   the regular commit hook is enough.

4. **Cross-session conflicts?** If two containers for the same group
   run concurrently (shouldn't happen — GroupQueue enforces serial
   per-group) and both write memory, the file-lock in `memory_tool.py`
   prevents corruption. We should port the `fcntl.flock` pattern in the
   Go impl — same semantics via `syscall.Flock`.

## Phase 1 verification

1. `make build && make -C ant build` — clean
2. `make test` — including new `ipc/memory_test.go`, `ipc/scan_test.go`,
   `ipc/skillmanage_test.go`
3. Manual smoke on a dev instance:
   - Send agent a message that triggers `memory add`
   - Verify `<groupDir>/.claude/MEMORY.md` contains the entry
   - Restart container for that group; verify next session's system
     prompt includes the entry
   - Attempt `memory add` with a role-hijack payload; verify scanner
     rejects with clear error
   - Verify `skill_manage create` produces a valid skill dir

## Estimated LOC

- `ipc/memory.go`: 180
- `ipc/skillmanage.go`: 250
- `ipc/scan.go`: 60
- Tests: 300
- `ant/src/index.ts` edits: 20
- Skill updates: 40
- **Total**: ~850 LOC, one weekend to ship

## References

- `/home/onvos/app/refs/hermes-agent/tools/memory_tool.py`
- `/home/onvos/app/refs/hermes-agent/tools/skill_manager_tool.py`
- `/home/onvos/app/refs/hermes-agent/tools/skills_guard.py`
- `/home/onvos/app/refs/hermes-agent/agent/memory_manager.py`
- `.diary/20260411.md` — 16:45 peel notes
