# Workspace layout

Platform mounts use FHS canonical locations (v0.45.11+). Your home
(`~`, `/home/node/`) and its subdirs are unchanged.

| Path                  | Contents                                                       | Access                                      |
| --------------------- | -------------------------------------------------------------- | ------------------------------------------- |
| `/opt/arizuko`        | arizuko source (canonical skills, changelog, migrations)       | read-only, all groups                       |
| `~/` (`/home/node`)   | home + cwd — group files, .claude/, diary, media               | read-write                                  |
| `~/public_html/`      | public web slot, projects to `<data>/web/pub/<folder>/`        | read-write                                  |
| `~/private_html/`     | OAuth web slot, projects to `<data>/web/priv/<folder>/`        | read-write                                  |
| `/var/lib/www`        | unified public web tree (RO browse of every group's `web/pub`) | read-only (tier 0 read-write)               |
| `/var/lib/share`      | shared group memory                                            | RO/RW per grant                             |
| `/run/ipc`            | gateway↔agent IPC (input/, gated.sock MCP server)              | read-write                                  |
| `/var/lib/groups`     | all group dirs (root only — for migrate)                       | read-write, tier 0 only                     |
| `/mnt/<name>`         | operator-configured extra mounts                               | varies                                      |
| `~/.claude`           | agent memory: skills, CLAUDE.md, sessions                      | read-write                                  |

Your home is `~`. NEVER use `/home/node/` in paths.

## URL view

`~/public_html/<x>` and `~/private_html/<x>` are bind-mount views of
the unified web tree:

| Container path                    | URL                                                   | Auth |
| --------------------------------- | ----------------------------------------------------- | ---- |
| `~/public_html/<x>`               | `/pub/$ARIZUKO_GROUP_FOLDER/<x>`                      | none |
| `~/private_html/<x>`              | `/priv/$ARIZUKO_GROUP_FOLDER/<x>`                     | JWT  |
| `/var/lib/www/<other>/<x>` (read) | `/pub/<other>/<x>` (other groups' content, RO)        | none |

`https://$WEB_HOST/pub/<X>` and `https://$WEB_HOST/<X>` serve the
SAME file (the second JWT-rewrites to `/pub/<X>`).
`https://$WEB_HOST/priv/<X>` serves a DIFFERENT file from
`<data>/web/priv/`.

## Root group only

```bash
ls /opt/arizuko/
cat /opt/arizuko/CHANGELOG.md
git -C /opt/arizuko log --oneline -10
ls /var/lib/groups/                # all groups visible (tier 0)
ls /var/lib/www/                   # whole public tree, RW for tier 0
```
