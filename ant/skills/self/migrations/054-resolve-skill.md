# 054 — /resolve skill replaces /dispatch

`/resolve` is seeded automatically. Strip stale `/dispatch` references:

```bash
sed -i '/Run `\/dispatch`/d' ~/.claude/CLAUDE.md 2>/dev/null
```
