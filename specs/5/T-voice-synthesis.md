---
status: shipped
---

# Voice synthesis (TTS output)

Shipped 2026-05-01.

Agent text → spoken voice message via channel. `ttsd` daemon is a thin
OpenAI-compatible HTTP proxy in front of a Kokoro-FastAPI backend
(default `http://kokoro:8880`); operators who prefer Piper, Coqui, or
OpenAI cloud override `TTS_BACKEND_URL` and skip the bundled service.
Gateway caches by `sha256(text+voice+model)` at `<data_dir>/tts/<hash>.ogg`.

New MCP tool `send_voice(chatJid, text, voice?)` is distinct from
`send_file` because each platform's voice primitive is distinct from
its file/audio primitive: Telegram has `sendVoice` (push-to-talk) vs
`sendAudio` (music); WhatsApp has `audio + ptt:true` vs plain
`audio + mimetype:...`; Discord has no native PTT but renders inline
audio attachments via ContentType.

Implemented as `Channel.SendVoice(jid, audioPath, caption)` in
`core.Channel`. Adapters that support voice implement it natively
(teled, discd, whapd); others embed `chanlib.NoVoiceSender` to
return `chanlib.ErrUnsupported`, mapped by the IPC layer to a 501
the agent can fall back from to plain `send`.

Config: `TTS_ENABLED`, `TTS_BASE_URL`, `TTS_VOICE`, `TTS_MODEL`,
`TTS_TIMEOUT`. Voice resolution: explicit arg > `voice:` frontmatter
in `~/SOUL.md` > `TTS_VOICE` env. Text capped at 5000 chars.

Rationale: users who send voice expect voice replies. Whisper handles
input already (`VOICE_TRANSCRIPTION_ENABLED`); this is symmetric.
Agent decides when to speak (voice-first persona, or matching the
user's modality), not the system.

See agent migration 088 + CHANGELOG [Unreleased].
