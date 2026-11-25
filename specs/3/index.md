---
status: shipped
---

# specs/3 — features & extensions

No spec is open in this phase. The group `send` rule is in `ipc/README.md`
§ "Who may call what".

Merged or moved out of this phase:

| Was                         | Now                                                                                                             |
| --------------------------- | --------------------------------------------------------------------------------------------------------------- |
| `8-web-virtual-hosts.md`    | `proxyd/README.md` — `web_host` column, `set_web_host` IPC and CLI all retired; per-world hosts are derived     |
| `V-platform-permissions.md` | `auth/README.md` — the proposed `platform_grants` table was never built; permissions are routes-derived         |
| `J-container-commands.md`   | Deleted — never shipped. `container.Input` has no `Command` field and `scheduled_tasks` has no `command` column |
