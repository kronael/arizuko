# Multi-Agent Commit Coordination

When multiple agents work on a single repo (e.g. kanipi dev with
parallel Claude Code sessions, or openclaw-style multi-agent), their
commit workflows overlap: staging collisions, pre-commit hook
contention, index.lock races.

## Problem

1. Agent A runs `git add` on its files, Agent B runs `git add` on its
   files — both see each other's changes in `git status`
2. Pre-commit hooks (prettier, typecheck) run on ALL staged files,
   including the other agent's WIP that may not compile yet
3. `index.lock` contention when commits overlap in time
4. Agents may accidentally stage or commit files they didn't touch

## Prior Art: OpenClaw

OpenClaw has three layers for commit coordination:

### Layer 1: Git pre-commit hooks (`.pre-commit-config.yaml` + `prek`)

Standard git hooks via `prek install` (their pre-commit framework):

- File hygiene: trailing-whitespace, end-of-file-fixer, check-yaml,
  check-added-large-files (500kb), check-merge-conflict, detect-private-key
- Secret detection: Yelp's detect-secrets with exclude patterns
- Shell: shellcheck (error-level only)
- CI lint: actionlint + zizmor (security audit for GH Actions)
- Python: ruff for skills scripts
- Project: pnpm audit, oxlint (type-aware), oxfmt, swiftlint, swiftformat

Same pattern as kanipi's prettier/typecheck hooks.

### Layer 2: `scripts/committer` wrapper

The multi-agent-safe commit tool — agents are told in AGENTS.md to
use this instead of raw git:

```bash
git restore --staged :/              # clean slate
git add --force -- file1 file2       # only my files
git commit -m "msg" -- file1 file2   # scoped commit
```

Safety guards:

- Blocks `.` as argument (prevents staging entire repo)
- Blocks `node_modules` paths
- Validates all files exist before staging
- `--force` flag removes stale `index.lock` on retry
- Captures stderr to detect lock errors specifically
- Never touches unrecognized files

### Layer 3: Event hooks system (24 plugin hook types)

NOT git hooks — openclaw-specific runtime event listeners:

**Discovery** (precedence order):

1. `<workspace>/hooks/` — per-agent, highest priority
2. `~/.openclaw/hooks/` — user-installed, shared
3. `<openclaw>/dist/hooks/bundled/` — shipped defaults

**Hook structure**: `HOOK.md` (YAML frontmatter) + `handler.ts`

**Relevant hook types**:

- `before_tool_call` — can block tool execution
- `after_tool_call` — post-tool observation
- `tool_result_persist` — synchronous result transform
- `session:compact:before/after` — compaction events
- `command:new/reset/stop` — command lifecycle
- `message:received/sent` — message lifecycle

**Bundled hooks** (4 shipped):

- `session-memory` — saves context on `/new`
- `bootstrap-extra-files` — injects workspace files
- `command-logger` — logs commands to file
- `boot-md` — runs BOOT.md on gateway start

**Management CLI**:

```bash
openclaw hooks list/enable/disable/install/update
```

**Configuration** (`~/.openclaw/config.json`):

```json
{
  "hooks": {
    "internal": {
      "enabled": true,
      "entries": {
        "session-memory": { "enabled": true },
        "my-hook": { "enabled": true, "env": { "MY_VAR": "val" } }
      },
      "load": { "extraDirs": ["/path/to/more/hooks"] }
    }
  }
}
```

### CLAUDE.md / AGENTS.md rules

- "commit" = scope to your changes only
- "commit all" = everything, in grouped chunks
- Unrecognized files → leave alone, don't mention
- Formatting-only churn → auto-resolve, no confirmation
- No stash, no worktree, no branch switching
- `git pull --rebase` OK for integration, never discard

### Key difference from kanipi

OpenClaw enforces scoped commits via a **script** (hard to
misinterpret). Kanipi enforces via **skill instructions** (soft).
The script is more reliable — agent can't accidentally `git add .`
because the script rejects it.

OpenClaw has NO stop hook that nags about uncommitted changes.
Their commit discipline comes from the committer script being the
only sanctioned way to commit.

### What to adopt, what to skip

**Adopt**:

- Committer script pattern (restore-staged + scoped add + scoped commit)
- CLAUDE.md multi-agent safety rules (no stash, scoped staging)
- index.lock retry with `--force`
- File existence validation before staging

**Adopt simplified**:

- Event hooks — kanipi already has PreCompact/Stop/PreToolUse via
  SDK hooks in agent-runner. Don't need openclaw's 24-type system.
  But the hook discovery pattern (workspace → user → bundled) and
  HOOK.md frontmatter are worth considering for v2 extensibility.
- Hook management CLI — not needed now, but the enable/disable
  pattern per config.json is clean.

**Skip**:

- `prek` framework — kanipi uses standard pre-commit, works fine
- Secret detection hook — kanipi agents run in containers, lower risk
- Plugin hook types for subagent lifecycle — kanipi delegates via
  GroupQueue, different model
- swiftlint/oxlint specifics — language-specific, not relevant

## Proposed: Commit Skill Multi-Agent Guards

Add to the global commit skill (synced to kanipi agent skill):

1. **Unstage before staging** — `git restore --staged :/` before
   `git add` to clear other agent's staged files
2. **Stage only your files** — explicit file list, never `.` or `-A`
3. **Handle index.lock** — if commit fails with lock error, wait 2s
   and retry once (stale lock from crashed agent)
4. **Ignore unrecognized changes** — `git status` shows files you
   didn't touch? Leave them alone, don't report them
5. **Scoped pre-commit** — stage only your files so hooks only check
   your changes (follows naturally from #1)
6. **Formatting retry** — if pre-commit reformats YOUR files,
   re-stage only those and retry; if it reformats OTHER files,
   ignore the reformatting

## Open Questions

### Q1: Tracking "my files"

How does an agent know which files are "mine"? Options:

- **Implicit**: files the agent created or edited this session
  (Claude Code tracks this internally via tool use history)
- **Explicit**: agent maintains a list (fragile, state management)
- **Diff-based**: `git diff --name-only` before and after work
- OpenClaw approach: committer takes explicit file args — agent
  must pass them. Simple and correct.

### Q2: Wrapper script vs skill instructions

- OpenClaw uses a bash script (`scripts/committer`) that agents call
- Kanipi uses a skill (SKILL.md instructions) that agents follow
- Script is more reliable (can't be misinterpreted) but less flexible
- Could do both: skill instructions + optional wrapper for safety

### Q3: Lock contention frequency

- How often do two agents actually commit simultaneously?
- In kanipi's model: agents are in separate containers on separate
  group folders — they rarely share a git repo
- In dev: user runs multiple Claude Code sessions on same repo —
  this is the real case
- Is a 2s retry sufficient, or do we need a proper lock queue?

### Q4: Pre-commit scope

- `git restore --staged :/` + selective `git add` means pre-commit
  only sees your files — but typecheck runs on ALL .ts files
  regardless of staging (it's `pass_filenames: false`)
- Typecheck may fail on another agent's half-written code
- Options: skip typecheck in multi-agent mode? Accept the failure?
  Only the committing agent's files should be valid...

### Q5: Global skill vs per-project

- The commit skill is global (`~/.claude/skills/commit/SKILL.md`)
- Multi-agent guards should be global (any repo could have this)
- But some guards (index.lock retry) are universally good
- Others (ignore unrecognized files) only apply when multiple agents
  are confirmed active — false positive risk in solo mode?

### Q6: Detection

- Should the skill detect multi-agent mode automatically?
- Signals: multiple Claude Code processes, multiple session dirs
  with recent activity, other agent's uncommitted changes visible
- Or just always apply the guards (they're safe in solo mode too)?

### Q7: Inserting the committer script

OpenClaw's committer lives at `scripts/committer` in the repo.
For kanipi agents running in containers, where does it go?

Options:

- **Global `~/.claude/scripts/committer`** — commit skill references
  it. Works for dev (direct Claude Code sessions). Needs manual
  install or dotfiles sync.
- **Container skill asset** — ship as `container/skills/commit/committer`
  alongside SKILL.md. Agent-runner copies skills to `~/.claude/skills/`
  on first spawn. Script available at `~/.claude/skills/commit/committer`.
  Skill instructions say "run this script instead of raw git".
- **Repo-local `scripts/committer`** — like openclaw. Each project
  that wants multi-agent safety adds the script. Commit skill detects
  its presence and uses it when available.
- **All three** — global for dev sessions, container skill for kanipi
  agents, repo-local for other projects. Same script, three install
  paths. Skill instructions: "use `scripts/committer` if present,
  else `~/.claude/skills/commit/committer`, else follow manual steps".

Precedence matters: repo-local > container skill > global.
Same pattern as openclaw's hook discovery (workspace → user → bundled).

### Q8: Skill + script relationship

If we ship a committer script, what stays in the skill vs moves
to the script?

- **Script**: restore-staged, scoped add, scoped commit, lock retry,
  file validation, block `.` and `node_modules`
- **Skill**: when to commit (cohesive chunk check), message format
  (`[section] Message`), marker handling, formatting retry logic,
  multi-agent awareness rules (ignore unrecognized files)
- The skill invokes the script for the actual git operations
- The skill handles the decision-making and message drafting
