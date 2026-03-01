---
name: reload
description: Restart the kanipi container or reload config. Use when asked to restart, reload, or refresh the instance.
---

# Reload

Restart the current kanipi instance by signaling the gateway process.

## Usage

Send SIGTERM to the gateway process. The container's restart policy
will bring it back with fresh config.

```bash
kill -TERM 1
```
