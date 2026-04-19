---
status: unshipped
---

# Voice synthesis (TTS output)

Agent text → spoken voice message via channel. `ttsd` daemon wraps a
backend (Kokoro local, OpenAI TTS, or similar): `POST /synthesize
{text, voice, format}` → audio bytes. Gateway caches by
`sha256(text+voice)` at `<data_dir>/tts/<hash>.ogg`.

New MCP tool `send_voice(chatJid, text, voice?)` mirroring
`send_file`. Gateway synthesizes + delivers through existing
`SendFile` path. Telegram's sendFile dispatches `.ogg` as audio; no
adapter changes required (whapd can gain `/send-voice` with `ptt:true`
later).

Config: `TTS_ENABLED`, `TTS_BASE_URL`, `TTS_VOICE`, `TTS_MODEL`.

Rationale: users who send voice expect voice replies. Whisper handles
input already (`VOICE_TRANSCRIPTION_ENABLED`); this is symmetric.
Separate `send_voice` tool matches every platform's API model
(Telegram/Discord/WhatsApp have distinct voice methods).

Unblockers: `ttsd` wrapper, `send_voice` in `ipc/`, CLAUDE.md
instruction for agent to call it when input was voice. Voice selection
via SOUL.md frontmatter (`voice:`) is a later refinement.
