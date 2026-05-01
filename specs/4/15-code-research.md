---
status: shipped
---

# Code Research Agent

Product config that turns a group into a codebase Q&A agent with background
research. Agent answers questions about a mounted codebase; when knowledge
base doesn't fully answer, it researches via `/find` and writes verified
fact files. In production as REDACTED Atlas since March 2026.

## Architecture

### Tier model

```
atlas/              tier 1 — world admin
  atlas/support     tier 2 — Q&A agent
  atlas/support/web tier 3 — web dashboard (future)
```

### Mount

Per-group mount config lives in `groups.container_config` JSON
(`core.GroupConfig.Mounts`); `mountsec.ValidateAdditionalMounts`
validates each entry and routes it to `/workspace/extra/<name>` inside
the container (`container/runner.go:516`). The ContainerPath defaults
to the basename of the host path. Read-only by default unless the
allowlist root permits read-write.

### Knowledge base

`facts/` dir in group folder. Each fact file has YAML frontmatter
(see `ant/skills/find/SKILL.md` for the canonical schema):

```yaml
---
topic: <specific topic>
category: <top-level category>
verified_at: <ISO timestamp>
sources:
  - <URL or file:line or commit SHA>
summary: >
  <one sentence — used by /recall-memories for fast grep>
---
```

### /find skill

Two-phase (`ant/skills/find/SKILL.md`):

1. Research — subagent explores codebase + web, writes draft fact files
2. Verify — subagent (per batch of 5) refutes each finding before
   stamping `verified_at`

Results written to `facts/<slug>.md`. Agent answers from verified facts.
Research+verify prompts ported from eliza-plugin-evangelist;
see `specs/res/eliza-prompts.md`.

## SYSTEM.md

Reference system prompt for code research groups. Replaces Claude Code
default for user-facing research agents. Ships as a world template in
`container/worlds/code-researcher/` when worlds are implemented.

```markdown
# {AGENT_NAME}

You are a codebase research assistant. You answer questions about the
codebases mounted in your workspace using evidence from your knowledge
base and direct code exploration.

Read SOUL.md on session start for your persona and voice.

## Knowledge-First Rule

Before every answer, scan `facts/` headers in `<think>`:

A fact is relevant ONLY if it answers the question 100% correctly with
only trivial application needed. No interpretation, no inference, no
"probably matches". If you have any doubt, the fact is NOT relevant.

Decision tree:

- Fact fully answers + fresh (verified_at < 14 days) → answer from it
- Fact fully answers but stale → run `/find` to refresh, then answer
- No fact fully answers → run `/find` to research, then answer

Always deliberate in `<think>` before answering:

1. List candidate facts found by scanning headers
2. For each candidate: does it directly answer? what gap remains?
3. Verdict: use, refresh (`/find`), or research from scratch

Never guess. Never claim code behavior without citing file:line evidence.

## Research

When no fact is relevant:

1. Emit `<status>researching...</status>`
2. Run `/find` with the specific question
3. Wait for results, answer from new facts

## Evidence Standard

- Always cite file:line when referencing code
- Quote code snippets when they clarify
- Say so honestly if evidence is missing
- Never fabricate paths, names, or line numbers

## What You Do NOT Do

- Do not run builds, tests, or modify files
- Do not execute arbitrary commands
- Do not access developer tools (git status, npm) in responses
```

## Howto: Manual setup

```bash
arizuko create myresearch
arizuko group <inst> add <jid> atlas
arizuko group <inst> add <jid> support atlas/support
```

Tier is derived from folder depth (1 = `atlas`, 2 = `atlas/support`).
Edit `/srv/data/arizuko_myresearch/.env` for channel tokens
(`TELEGRAM_BOT_TOKEN=...`). Mounts are configured per-group on the
`groups.container_config` JSON column (no env var); attach the
codebase by populating `Mounts` in the group's stored config.

Copy the reference SYSTEM.md above into
`/srv/data/arizuko_myresearch/groups/atlas/support/SYSTEM.md`,
replacing `{AGENT_NAME}`.

Write SOUL.md with persona. Seed `facts/` with initial .md files.
Set channel tokens. `sudo systemctl start arizuko_myresearch`.

### Example: REDACTED Atlas (production)

```
Instance: REDACTED
World: atlas
Groups: atlas (tier 1), atlas/support (tier 2)

Mounts:
  /srv/data/REDACTED-repos → /workspace/extra/codebase (ro)

facts/: 40+ verified facts
Channel: Telegram, requires_trigger=0
```

## Open questions

- `/find` skill has no hard timeout — relies on container session timeout.
- Dedup across re-research (agent checks existing facts first).
- Semantic search (embeddings) deferred; strict relevance rule compensates.
- Multi-codebase: `GroupConfig.Mounts` is a slice, supports any number
  of entries.
