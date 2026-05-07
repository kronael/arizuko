---
name: reload
description: Restart the arizuko container or reload config.
when_to_use: Use when asked to restart, reload, or refresh the instance.
user-invocable: true
---

# Reload

Send SIGTERM to PID 1; the container's restart policy brings it back with
fresh config.

```bash
kill -TERM 1
```
