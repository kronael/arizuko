---
name: readme
description: >
  /readme — launch @readme agent to update documentation. USE for
  "/readme", "update the docs", "sync README", README.md / ARCHITECTURE.md
  / CHANGELOG.md drift after a feature shipped. NOT for new doc creation
  from scratch (write directly).
user-invocable: true
---

Launch the @readme agent (Task tool, subagent_type: readme) to update README, ARCHITECTURE, and documentation files.

## Extend, don't restart

**ALWAYS** look for an existing doc surface before creating a new one.
Three rings: agent (`CLAUDE.md`), contributor (root UPPERCASE), package
(per-pkg `README.md`). Most features extend an existing surface; new
top-level pages are the exception.

**NEVER** create a new how-to or reference page if a closely-related
one exists and the addition would fit as a section there. Two pages
covering one concern drift silently — same failure as "one renderer,
many sinks".

Example: documenting `SEND_DISABLED_GROUPS` extends `gateway/README.md`
"Mute mode" + an "Audit mode" subsection in `pub/howto/index.html`,
not a fresh `pub/howto/audit-mode.html`.
