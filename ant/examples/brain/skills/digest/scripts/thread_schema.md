# Thread distill — prompt + fixed output schema (digest-discourse step 3)

You distill ONE Discord thread for a daily/weekly digest. Input: the file path of
`threads/<id>.xml` (human rows from the thread root, oldest first) and the window
`START..END`, plus the previous run's `threads/<id>.md` if it exists (PREV).

The agent is NOT a participant. Its rows are already removed. A row with
`endorsed_by="..."` is agent text that a human applauded: treat it as the
position of that human (`<member> (endorsed): "<quote>"`), never as the agent's.
NEVER mention the agent, its answers or its reversals.

Quote; do not paraphrase a position. Write the result to `threads/<id>.md`.
Output EXACTLY these keys, in this order:

```
thread: <id> | "<root quote, ≤ 10 words>" | rows <N> | span <first> → <last>
question: <one line — what the thread is deciding>
positions:
  - <member> @<time>: "<quote ≤ 25 words>"      # one line per distinct human position
result: <what the humans ended up with, ≤ 15 words; "none yet" if no outcome>
why: <the reason a human gave for the result, quoted with owner; "not stated" if none>
vs before: started | finished | changed (<old> → <new>) | continued   # compare with PREV file or START
open: <disagreements still standing, who vs who, one line each; "none">
status: open | agreed | parked      # agreed ONLY if the last human message agrees with the result
last human: <member> @<time>: "<quote ≤ 20 words>"
```

Rules: `started` = root inside the window; `finished` = status moved to agreed or
parked inside the window; `changed` = a result differs from PREV or from the first
position in the thread. NEVER mark `agreed` when the last human message pushes
back, asks a question, or changes topic. NEVER add a conclusion of your own; the
reviewer rejects conclusions without a quote. Max 30 lines.
