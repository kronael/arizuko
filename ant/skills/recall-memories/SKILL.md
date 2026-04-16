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

1. In `<think>`, expand the question into ~10 search terms
2. Run `recall "term"` for each term via Bash
3. Collect all results (deduplicate by path)
4. Spawn an Explore subagent with the collected results + question
5. Explore judges which are relevant and why

## After results

Deliberate in `<think>` (mandatory):

1. List matched files
2. For each: what does it say? Does it answer? What gap?
3. Verdict: use it, refresh via `/facts`, or research fresh

**Weight corrections over conclusions.** Trust user corrections
verbatim; re-derive conclusions fresh from corrections + current
context. Never reuse a prior agent summary as a fact.

## When to use

- Technical question → /recall (searches facts/)
- Question about a person → /recall (searches users/)
- Question about recent work → /recall (searches diary/, episodes/)
- Trivial message → skip
