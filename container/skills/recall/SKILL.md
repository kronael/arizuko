---
name: recall
description: Search knowledge stores for relevant information.
user_invocable: true
arg: <question>
---

# Recall

Search `facts/`, `diary/`, `users/`, `episodes/` for information
relevant to a question. Read-only — never writes files.

## Protocol

Check if `recall` CLI is available (`which recall 2>/dev/null`).

### With CLI (v2)

1. In `<think>`, expand the question into ~10 search terms
2. Run `recall "term"` for each term via Bash
3. Collect all results (deduplicate by path)
4. Spawn an Explore subagent with the collected results + question
5. Explore judges which are relevant and why

### Without CLI (v1 fallback)

Spawn an Explore subagent that:

1. Greps `summary:` in `*.md` across facts/, diary/, users/, episodes/
2. Reads each summary value
3. Judges: does this summary relate to the query?
4. Returns matches: file path, store name, why it matches

## After results

Deliberate in `<think>` (mandatory):

1. List matched files
2. For each: what does it say? Does it answer? What gap?
3. Verdict: use it, refresh via `/facts`, or research fresh

## When to use

- Technical question → /recall (searches facts/)
- Question about a person → /recall (searches users/)
- Question about recent work → /recall (searches diary/, episodes/)
- Trivial message → skip
