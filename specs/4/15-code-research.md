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

Instance `.env`:

```env
EXTRA_MOUNTS=/path/to/repo:codebase:ro
```

Gateway appends: `/path/to/repo → /workspace/extra/codebase` (ro).

### Knowledge base

`facts/` dir in group folder. Each fact file has YAML frontmatter:

```yaml
---
slug: validator-bonds-overview
topic: validator-bonds
verified_at: 2026-03-10T14:30:00Z
verification:
  status: verified
  confidence: high
  verified_count: 5
  rejected_count: 1
---
```

### /find skill

Two-phase:

1. Research — Opus subagent explores codebase + web, writes factset XML
2. Verify — Sonnet subagent tries to refute each finding

Results written to `facts/<slug>.md`. Agent answers from verified facts.
Research+verify prompts ported verbatim from eliza-plugin-evangelist;
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
arizuko group add myresearch atlas --tier 1
arizuko group add myresearch atlas/support --tier 2
```

Edit `/srv/data/arizuko_myresearch/.env`:

```env
EXTRA_MOUNTS=/path/to/target/repo:codebase:ro
TELEGRAM_BOT_TOKEN=...
```

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
  /srv/data/REDACTED-repos:codebase:ro

facts/: 40+ verified facts
Channel: Telegram, requires_trigger=0
```

## Open questions

- `/find` skill has no hard timeout — relies on container session timeout.
- Dedup across re-research (agent checks existing facts first).
- Semantic search (embeddings) deferred; strict relevance rule compensates.
- Multi-codebase: current `EXTRA_MOUNTS` supports comma-separated mounts.
