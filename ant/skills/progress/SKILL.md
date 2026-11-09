---
name: progress
description: Track longer work with the native checklist. NOT for one-shot replies.
when_to_use: "multi-step task, longer task, investigation, implementation, delegation, verification"
---

# Progress

- ALWAYS use the exposed native checklist: Claude task tools or TodoWrite;
  Codex update_plan.
- ALWAYS plan meaningful outcomes before work with several steps.
- ALWAYS track a delegated job as one parent milestone.
- ALWAYS mark the current step before starting it.
- ALWAYS mark a step complete only after checking its result.
- ALWAYS revise the checklist when scope changes or work becomes blocked.
- ALWAYS use short labels safe for everyone in the reply chat.
- NEVER put secrets or private source details in labels; ALWAYS describe
  the public outcome.
- NEVER post the checklist through reply or send; ALWAYS update its tool.
- NEVER add a checklist to a one-shot answer; ALWAYS answer directly.
- ALWAYS use the checklist internally when the platform cannot display it.
- NEVER invent an unavailable tool; ALWAYS report the missing capability.

## Planless turns and unsupported platforms

- Print `<status>` blocks outside `reply` and `send` for progress without a checklist.
  Say what the user gets next, never which tool, model or file.
  End with `reply` containing the answer, not a copy of the status.
- A channel that cannot both edit and delete shows no checklist.
