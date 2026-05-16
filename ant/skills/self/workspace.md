# Workspace layout

| Path                       | Contents                                                 | Access                                      |
| -------------------------- | -------------------------------------------------------- | ------------------------------------------- |
| `/workspace/self`          | arizuko source (canonical skills, changelog, migrations) | read-only, all groups                       |
| `~/` (`/home/node`)        | home + cwd — group files, .claude/, diary, media         | read-write                                  |
| `/workspace/share`         | shared global memory                                     | read-only for non-root, read-write for root |
| `/workspace/web`           | vite web app directory                                   | read-write                                  |
| `/workspace/ipc`           | gateway↔agent IPC (input/, gated.sock MCP server)        | read-write                                  |
| `/workspace/data/groups`   | all group dirs (for migrate; .claude/ inside each)       | read-write, main only                       |
| `/workspace/extra/<name>`  | operator-configured extra mounts                         | varies                                      |
| `~/.claude`                | agent memory: skills, CLAUDE.md, sessions                | read-write                                  |

Your home is `~`. NEVER use `/home/node/` in paths.

## Root group only

```bash
ls /workspace/self/
cat /workspace/self/CHANGELOG.md
git -C /workspace/self log --oneline -10
```
