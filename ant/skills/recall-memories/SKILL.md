---
name: recall-memories
description: >
  Search facts/, diary/, users/, episodes/ for relevant knowledge.
  Use for technical questions, person lookups, or recent work context.
  Read-only — never writes files.
user-invocable: true
arg: <question>
---

# Recall Memories

Search all memory stores for information relevant to a question.

## Protocol

Spawn an Explore subagent with the question. The subagent:

1. Greps `summary:` in `*.md` across `~/facts/`, `~/diary/`, `~/users/`,
   `~/episodes/`
2. Reads each summary, judges relevance to the question
3. Returns matches: file path, store name, why it matches

## After results

Deliberate in `<think>`: list matched files, what each says, whether it
answers, what gap remains. Verdict: use it, refresh via `/find`, or
research fresh.

**Weight corrections over conclusions.** Trust user corrections verbatim;
re-derive conclusions fresh. Never reuse a prior agent summary as a fact.
