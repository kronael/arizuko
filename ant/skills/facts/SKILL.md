---
name: facts
description: Research a topic and produce verified facts in facts/. Use when
  /recall-memories finds no match or when asked to research something.
user-invocable: true
arg: <question or topic to research>
---

# Facts

Research → verify → write to `facts/` for future recall via `/recall-memories`.
ALWAYS use subagents — never research in main context.

## Step 1: Research (subagent)

Tools: Read, Glob, Grep, WebSearch, WebFetch, Write.

- Search existing `facts/` for related knowledge first
- Search the web / codebase / DB as appropriate
- Prefer primary sources (official docs, source code, DB queries) over
  blog posts. Two corroborating primary sources > ten secondary ones.
- Write new fact files to `facts/` with YAML frontmatter:
  ```yaml
  ---
  topic: <specific topic>
  category: <top-level category>
  verified_at: <ISO timestamp>
  sources:
    - <URL or file:line or commit SHA — required, ≥1>
  summary: >
    <one sentence — used by /recall-memories for fast grep>
  ---
  <full content: claim, supporting evidence, direct quotes or excerpts>
  ```
- One fact per file, named by topic slug
- Include at least one inline citation per non-trivial claim in the body
- Stop after 3–10 new facts

## Step 2: Verify (subagent per batch of 5)

Tools: Read, Glob, Grep, WebSearch, WebFetch, Bash.

Each fact must pass **all** of these before `verified_at` is set:

1. **Source accessible** — every URL in `sources:` returns 200. Every
   file:line exists. Every commit SHA resolves.
2. **Claim matches source** — open the source, read it, confirm the
   fact's body is what the source actually says. Paraphrase drift is
   the most common failure mode.
3. **No contradiction** with other facts in `facts/`. If two facts
   disagree, one is wrong — investigate, delete the loser.
4. **Numbers recomputed** — any quantity, rate, size, or date in the
   body must be recomputed from source, not copied from a summary.
   Use `bash`/`python`/`jq` for arithmetic; don't trust head math.
5. **Volatile claims flagged** — version numbers, prices, API shapes,
   third-party service behaviour, security advisories. These decay
   fast; note the volatility in the body so future readers know to
   re-verify sooner than the default 14 days.

Delete facts that fail any check. Update `verified_at` on passing.
If a fact can't be verified at all, **do not write it** — an unverified
fact is worse than no fact.

## Step 3: Answer

Read surviving fact files, answer the user's original question. Cite
the fact file(s) inline (`facts/<slug>.md`) so the user can audit.
