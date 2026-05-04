---
status: planned
---

# Product: ops

DevOps / SRE assistant. Knows the infrastructure via runbooks in facts/
and the WebDAV workspace. Helps with incidents, explains alerts, suggests
fixes. Template at `ant/examples/ops/`.

## What it does

Answers infra questions grounded in actual runbooks. Executes scoped
shell commands when granted. Commits config changes after operator
confirmation. Runs a daily status digest via scheduled task.

Careful with action grants: `bash` is allowed but scoped. Destructive
commands (restart, delete, rollback) should be held for operator
confirmation — use `hold_if` CEL on the bash grant until HITL ships,
or document the manual confirmation step in CLAUDE.md.

## Skills

| Skill           | Required     |
| --------------- | ------------ |
| diary           | yes          |
| facts           | yes          |
| recall-memories | yes          |
| bash            | yes (scoped) |
| oracle          | recommended  |
| web             | recommended  |
| commit          | yes          |

## Channels

- Telegram — oncall phone (primary; alerts go here)
- Discord — ops team channel (shared incident thread)

## Persona (SOUL.md sketch)

Calm under pressure. Checks runbook before suggesting action. States
what it will run before running it. Never guesses at infrastructure state
— reads facts/ or asks.

## Proactive tasks (tasks.toml)

```toml
[[task]]
name    = "daily-digest"
cron    = "0 8 * * 1-5"
prompt  = "Check service health, summarise overnight alerts, post digest."
```

Requires `timed` daemon running and tasks.toml present in group folder.

## Depends on

- `davd` — runbooks and infra docs mounted into agent workspace.
- `timed` — for daily digest scheduled task.
- Careful grant config: `bash` grant with scoped `allow_paths` or
  `hold_if` on destructive patterns (rm, systemctl stop, docker rm).

## Web page

An SRE assistant that reads your runbooks and can run scoped shell
commands. Connect it to Telegram for oncall, Discord for the team,
and get a daily digest of service health every morning.

## Template files

```
ant/examples/ops/
  PRODUCT.md      name=ops, skills=[diary,facts,recall-memories,bash,oracle,web,commit]
  SOUL.md         persona (calm, cite runbook, confirm before action)
  CLAUDE.md       runbook (bash scope, confirmation gate, digest format)
  tasks.toml      daily-digest task
  facts/          operator seeds: service map, alert definitions, runbooks
```

## Open

- bash grant scope: whitelist commands vs allow_paths vs full shell
- Destructive-command gate: hold_if CEL vs manual confirmation in CLAUDE.md
- Alert ingestion: direct adapter vs webhook adapter (future)
