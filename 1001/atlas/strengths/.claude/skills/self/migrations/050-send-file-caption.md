# 050 — send_file caption parameter

`send_file` now accepts a `caption` parameter for message text delivered
alongside the file. Use `caption` instead of a separate `send_message` call.

## Changes

- New `caption` parameter on `send_file` MCP tool
- Telegram, WhatsApp, Discord: caption sent as native message caption
- CLAUDE.md updated: use `caption`, never follow up with `send_message`
