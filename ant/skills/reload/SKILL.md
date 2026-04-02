---
name: reload
description: Restart the arizuko container or reload config. Use when asked to restart, reload, or refresh the instance.
---

# Reload

Terminate the container to trigger a restart with fresh config.

## Usage

Send SIGTERM to PID 1 (the container entrypoint). The container's
restart policy will bring it back with fresh config.

```bash
kill -TERM 1
```
