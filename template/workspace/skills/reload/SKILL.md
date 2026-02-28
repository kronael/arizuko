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

To reload config without full restart, copy updated config and
signal the gateway:

```bash
cp /cfg/<instance>.json /home/node/.openclaw/openclaw.json
kill -HUP $(pgrep -f "openclaw.mjs gateway") 2>/dev/null || \
  kill -TERM 1
```
