---
status: shipped
---

# specs/3 — features & extensions

| Spec                                               | Status             | Hook                                                     |
| -------------------------------------------------- | ------------------ | -------------------------------------------------------- |
| [5-tool-authorization.md](5-tool-authorization.md) | superseded-in-part | tier tables kept as the source for `5/33`'s role bundles |

Merged or moved out of this phase:

| Was                         | Now                                                                                                                                       |
| --------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------- |
| `8-web-virtual-hosts.md`    | [`../5/V-web-vhosts.md`](../5/V-web-vhosts.md) — `web_host` column, `set_web_host` IPC and CLI all retired; per-world hosts are derived   |
| `V-platform-permissions.md` | [`../5/32-acl-unified.md`](../5/32-acl-unified.md) — the proposed `platform_grants` table was never built; permissions are routes-derived |
| `J-container-commands.md`   | Deleted — never shipped. `container.Input` has no `Command` field and `scheduled_tasks` has no `command` column                           |
