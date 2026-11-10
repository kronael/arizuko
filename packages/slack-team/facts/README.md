---
verified_at: 2026-05-17
---

# Channel knowledge base

This directory is the agent's primary source of truth. The agent reads
here BEFORE reaching for training data. Drop one Markdown file per
topic; the agent will cite `facts/<filename>.md:<line>` when answering.

## Conventions

- One topic per file. Short names: `pricing.md`, `runbook.md`,
  `oncall.md`, `customer-segments.md`.
- Frontmatter `verified_at: YYYY-MM-DD` so the agent can flag stale
  knowledge (older than 14 days → trigger `/find` to refresh).
- Be concrete. The agent quotes by file:line; precise facts cite
  cleanly, vague paragraphs cite badly.
- One claim per line where reasonable. Easier to cite, easier to
  refresh.

## What to put here vs `~/refs/`

- `facts/` — operator-curated, single source of truth per topic. The
  agent prefers these. Keep short.
- `refs/` — long-form reference docs (API specs, manuals, PDFs). The
  agent grep-reads these on demand. Keep raw.

## Starter topics (delete or fill)

- `pricing.md` — what you charge, billing model, upgrade paths
- `runbook.md` — common incident playbooks
- `oncall.md` — who's on this week, paging policy
- `customer-segments.md` — who buys what, account tiers
- `glossary.md` — internal jargon the agent should know

The agent doesn't need every file at once — pick what matters first.
Empty KB = web-only answers (if `web` skill is enabled) + escalation
prompts. Filling one file unlocks one topic.
