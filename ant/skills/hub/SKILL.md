---
name: hub
description: >
  Build a single-page knowledge hub on a topic by running parallel
  deep-research subagents, distilling, and assembling into HTML. Use
  when asked to "build a hub", "research hub", "knowledge hub", or
  "deep dive" on a frontier topic (biotech, AI, crypto, etc).
user-invocable: true
---

# Hub — knowledge hub builder

## 1. Plan

Create `tasks.md` in the project directory with numbered tasks, dependencies,
status checkboxes. Update throughout.

## 2. Parallel research

Launch one `deep-research` subagent per topic using Task tool
(`run_in_background: true`, max 3 in parallel). Each agent does 5 iterative
loops and writes to `./tmp/<topic>.md`.

Research prompt:

```
Research <TOPIC> for a comprehensive knowledge hub. Do 5 iterative
deepening loops, each building on the previous:
  1. Landscape: field, terminology, major players, current state, market size
  2. Mechanisms: how it works, key challenges, approaches and tradeoffs
  3. Literature: papers (title, authors, year, one-liner), key researchers,
     recent breakthroughs (last 2 years)
  4. Companies: who is building what, funding, stage, differentiation
  5. Synthesis: cross-references, gaps, emerging trends, contrarian takes
Output: structured markdown.
```

## 3. Scaffold in parallel

While research runs: check existing site patterns, scaffold `index.html`.

## 4. Distill

After research completes, launch `distill` subagent:

```
Distill N research documents into:
  1. TLDR (3-5 sentences, most important)
  2. Cross-cutting patterns
  3. Key tensions / tradeoffs
  4. What surprised you
Use 5/3 recursive summarization: summarize → merge → top-3 per group →
synthesize across → final.
```

## 5. Assemble

Single-page HTML, organized into:

- TLDR (distilled, top)
- Topic deep-dives (one card per research area)
- Companies table, key papers, guidebooks, people
- Cross-cutting patterns

Design: dark monospace theme, sticky section nav, cards, tables, status tags,
inline CSS, mobile-responsive, no JS frameworks.

## 6. Deploy

Drop `index.html` into `/workspace/web/pub/<name>/`. Live at
`https://$WEB_HOST/pub/<name>/`.

## Rules

- ALWAYS save raw research to `./tmp/` before assembling
- ALWAYS run research agents in parallel
- ALWAYS distill before final assembly — never paste raw research
- ALWAYS update `tasks.md` as work progresses
