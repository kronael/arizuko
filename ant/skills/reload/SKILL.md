---
name: reload
description: >
  Restart this container (SIGTERM PID 1) so it picks up fresh config.
  USE for "restart", "reload", "refresh the instance". NOT for restarting
  another service or container (operator-only).
user-invocable: true
---

# Reload

Send SIGTERM to PID 1; the container's restart policy brings it back with
fresh config.

```bash
kill -TERM 1
```
