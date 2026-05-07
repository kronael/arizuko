---
name: voice
description: Reference for the `send_voice` MCP tool — voice-name resolution, platform support, length cap.
when_to_use: >
  Use when the last inbound message was voice/audio, when `~/SOUL.md`
  has `voice:` set, when the user asks for a spoken reply, or before
  calling `send_voice` for the first time.
---

# Voice

`send_voice` synthesizes `text` via the configured TTS backend and delivers as a platform-native voice message. Prefer `send`/`reply` otherwise — voice is heavier and not searchable.

## Voice resolution (highest precedence first)

1. Explicit `voice:` arg passed to `send_voice`
2. `voice:` field in the group's `~/SOUL.md` YAML frontmatter
3. Instance default `TTS_VOICE` env (typically `af_bella` for Kokoro)

Empty at every step → tool errors with "no voice configured".

## Platform support

| Platform | Mode                          | Adapter behaviour            |
| -------- | ----------------------------- | ---------------------------- |
| telegram | push-to-talk voice note       | native sendVoice             |
| whatsapp | push-to-talk voice note       | native PTT                   |
| discord  | audio attachment              | `audio/ogg` file in channel  |
| others   | unsupported                   | adapter returns `ErrUnsupported` — fall back to `send` |

On `ErrUnsupported`, `send_voice` returns a structured unsupported
response. Catch it and re-deliver as text via `send` / `reply`.

## Constraints

- `text` cap: **5000 chars**. Trim or split before calling.
- No caption / accompanying text — voice stands alone. Don't pair with `send`.
- Not for music or pre-recorded audio files; use `send_file` for those.

## Call shape

```
send_voice chatJid:="telegram:user/<id>" text:="..." [voice:="<name>"]
```

Returns `{"ok": true, "id": "<platform-id>"}`. The outbound is recorded
in the local DB under the calling folder.
