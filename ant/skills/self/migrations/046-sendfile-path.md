# 046 — send_file: home dir is group dir

The agent home (`~/`) is the group directory, shared with the gateway.
Files under `~/` are accessible to `send_file`. Use `~/tmp/` for temp
files. Fix stale `/workspace/group` references:

```bash
sed -i 's|/workspace/group/tmp|~/tmp|g; s|under /workspace/group/ or /workspace/media/|under ~/|g' ~/.claude/CLAUDE.md
mkdir -p ~/tmp
```
