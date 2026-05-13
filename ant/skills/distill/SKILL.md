---
name: distill
description: >
  /distill — launch @distill agent to extract essence via recursive 5/3
  summarization. USE for "/distill", "summarize this", "compress this
  transcript", "extract the essence", long-form transcript reduction.
  NOT for skill creation from history (use learn).
user-invocable: true
---

Launch the @distill agent (Task tool, subagent_type: distill) to distill content through recursive summarization using the 5/3 approach.

## Shape vs. per-turn context reducers

This is the **agent-invoked**, on-demand half of arizuko's
context-reducer equivalent (the cron-driven half is `/compact-memories`).
Other systems — e.g. muaddib's `src/rooms/command/context-reducer.ts` —
run a cheap-model condenser *automatically per-turn* over the running
history. arizuko's `/distill` is the inverse cadence: the agent decides
when to compress, the user picks the input, and the output is durable
content (skill, fact, episode) rather than an ephemeral
prompt-injection. Same problem (token budget vs. fidelity), different
trigger point.
