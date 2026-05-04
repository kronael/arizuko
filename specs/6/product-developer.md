---
status: planned
---

# Product: developer

Coding assistant with access to the team's codebase via WebDAV.
Template at `ant/examples/developer/`.

## What it does

Reviews code, debugs problems, explains behaviour, suggests fixes.
Reads the actual codebase mounted via davd — answers are grounded in
real files, not hypothetical structure. Commits reviewed changes when
granted. Sub-queries via oracle for large searches.

## Skills

| Skill           | Required    |
| --------------- | ----------- |
| diary           | yes         |
| facts           | yes         |
| recall-memories | yes         |
| commit          | yes         |
| bash            | yes         |
| find            | yes         |
| oracle          | recommended |
| web             | recommended |

## Channels

- Telegram — personal dev assistant
- Discord — team channel (one group per repo or team)
- slink — web widget for teams without Discord

## Persona (SOUL.md sketch)

Precise, cites file paths and line numbers. Asks for clarification before
destructive suggestions. Summarises diffs before committing.

## Depends on

- `davd` — WebDAV workspace mounted into the agent container so the
  agent can read/write repo files. Without davd the agent answers from
  memory only.
- `oracle` recommended — codex sub-queries for grep-scale searches
  across large codebases.

## Web page

A coding assistant that reads your actual codebase, not a generic model.
Mount your repo via WebDAV, connect to Telegram or Discord, and ask it
to review, debug, or explain any file.

## Template files

```
ant/examples/developer/
  PRODUCT.md      name=developer, skills=[diary,facts,recall-memories,commit,bash,find,oracle,web]
  SOUL.md         persona (precise, cite paths)
  CLAUDE.md       runbook (mount path conventions, commit gate)
  facts/          seed with repo conventions, coding standards
```

## Open

- Commit grant scope: should `commit` be `hold_if` gated pending HITL
- bash scope: whitelist vs full shell (operator choice at deploy time)
- Multi-repo: one group per repo vs shared group with multiple mounts
