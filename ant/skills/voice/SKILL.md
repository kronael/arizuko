---
name: voice
description: Reference for the `send_voice` MCP tool — when to use it,
  voice-name resolution, platform support, and length cap. Invoke when
  the user sent voice and expects voice back, when the persona is
  voice-first, or before calling `send_voice` for the first time.
---

# Voice

`send_voice` synthesizes `text` via the configured TTS backend and
delivers it as a platform-native voice message.

## When to use

- Last inbound message was a voice/audio attachment
- `~/SOUL.md` frontmatter has `voice:` set (persona is voice-first)
- User explicitly asked for spoken reply
- Otherwise prefer `send` / `reply` — voice is heavier and not searchable

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
