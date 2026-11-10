---
name: digest-discourse
description: >
  Research half 2 of the digest: THE DISCORD DISCOURSE. Reads the Discord
  threads active in one digest window (whole threads, from the root; agent
  messages dropped unless a human applauded them) and writes
  `<run>/discourse/discourse.md` — per conversation: question, positions,
  result, why, change vs before; per person: what they argued and decided.
  Runs in parallel with digest-work; the digest skill combines both. USE when
  the digest orchestrator starts it, or "what was discussed", "results of the
  conversations". NOT for GitHub work (use digest-work), NOT for posting (use digest).
user-invocable: true
arg: <run-dir>
managed: local
---

# digest-discourse — what was discussed and decided

Input: a run dir with `window.txt` (digest step 0). Output:
`<run>/discourse/discourse.md` and `<run>/discourse/DONE`. Read ONLY Discord.
NEVER read GitHub here — the work half does that, and the two meet only in the
digest combine step. Scripts: `$(dirname <this file>)/../digest/scripts`.

The agent is NOT a participant. `gather_discord.py` drops its rows; a
kept row has `endorsed_by=` and counts as that human's position. NEVER report
the agent as a speaker or source.

## Steps

1. `source <run>/window.txt; mkdir -p <run>/discourse`. If
   `<run>/discourse/DONE` exists, stop: this half is done (resume rule).
2. Gather: `<scripts>/gather_discord.py <run>` (add `--no-backfill` only on a
   rerun). Output in `<run>`: `main.xml`, `threads/<id>.xml`, `threads.json`,
   `members.json`, `channels.json`.
3. Distill each thread to `<run>/threads/<id>.md` with the schema in
   `<scripts>/thread_schema.md`. Threads with > 8 human rows: one `distill` or
   `sonnet` subagent each, at most 2 in parallel (the `global` subagent cap);
   smaller threads inline. Give the subagent the xml path, START/END,
   `$PREV/threads/<id>.md` if it exists, and the schema file — nothing else.
   Check that every file has all keys.
4. Rows outside threads (`main.xml`): group by topic. Keep a topic only when a
   human asks, decides, reports a result, or disagrees. Drop social chatter.
5. Per person (every name in `members.json`, quiet ones too): what they
   argued, asked for, decided or reported — each line a quote with time.
   Then read `$PREV/discourse/discourse.md` and every `$HISTORY/*.md`: for
   each conversation or position named there, write what happened to it in
   this window (closed, changed, still open, dropped), with the date.
6. Write `<run>/discourse/discourse.md` in the schema below, then
   `touch <run>/discourse/DONE`.

## discourse.md schema

```
# discourse — <LABEL> — rows <human N> in <thread count> threads, agent rows dropped <N>
## conversations
### <thread id> — <question, ≤ 8 words> — <status>
result: <≤ 15 words, or "none yet">
why: <member> @<time>: "<quote>"   (or "not stated")
vs before: started | finished | changed (<old> → <new>) | continued
dissent: <who vs who, or "none">
### main — <topic ≤ 6 words>
…same keys…
## people
### <person> — rows <N>
- argued: "<quote>" @<time>
- decided/reported: "<quote>" @<time>
- asked for: "<quote>" @<time>
## open questions
- <question> — raised by <member> @<time>, no answer
```

## Rules

- ALWAYS read a thread from its root when any row falls in the window.
- ALWAYS quote with owner + time; NEVER paraphrase a position as a fact.
- NEVER write `agreed/closed/decided` unless the last human message agrees.
- NEVER read GitHub, diary or episodes in this skill; NEVER mention the agent.
- NEVER post anything. The digest skill posts.
- Max 120 lines.
