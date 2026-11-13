---
name: recall-memories
description: >
  Search stored knowledge — `~/facts/`, `~/diary/`, `~/users/`,
  `~/episodes/` — for relevant content. Read-only. USE for technical
  questions, person lookups, recent-work context, "what do I know
  about X". NOT for live chat history (use recall-messages) or fresh
  research (use find).
user-invocable: true
arg: <question>
---

# Recall Memories

The newest `~/diary/` and `~/episodes/` file of each level is already in the
prompt of a new task as `<memory scope="folder">`; recall reads the rest.

## Protocol

Spawn an Explore subagent with the question. The subagent:

1. Greps `summary:` lines and file names in `*.md` across `~/facts/`,
   `~/diary/`, `~/users/`, `~/episodes/`; a file with no `summary:` is found
   by its name only
2. Reads each summary, judges relevance to the question
3. Returns matches: file path, store name, why it matches

## After results

Deliberate in `<think>`: list matched files, what each says, whether it
answers, what gap remains. Verdict: use it, refresh via `/find`, or
research fresh.

**Weight corrections over conclusions.** Trust user corrections verbatim;
re-derive conclusions fresh. Never reuse a prior agent summary as a fact.
