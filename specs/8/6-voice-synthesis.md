# 6-voice-synthesis — TTS output from agent

**Status**: planned (open questions)

Voice transcription (Whisper, MEDIA_ENABLED) already handles input.
This spec covers the output direction: agent text → spoken voice message
delivered back to the user via the channel.

---

## Motivation

Channels that support audio (Telegram, WhatsApp, Discord) can receive
voice notes or audio files. Users who send voice messages expect voice
replies. An agent that only replies in text breaks the conversational
medium.

---

## What it looks like

1. User sends a voice note on Telegram
2. Whisper transcribes it → text in agent prompt
3. Agent responds with text
4. Gateway detects "this conversation started as voice" or "agent asked for voice"
5. TTS converts agent text to audio
6. Adapter sends audio file back to the user (Telegram voice note / WhatsApp audio)

---

## Components

### TTS service

External process (like Whisper for input). Accepts text, returns audio file.

Options:

| Service          | Type  | Quality | Cost        | Self-hosted |
| ---------------- | ----- | ------- | ----------- | ----------- |
| OpenAI TTS       | API   | high    | paid        | no          |
| ElevenLabs       | API   | highest | paid/limits | no          |
| Kokoro-82M       | local | good    | free        | yes         |
| Piper            | local | ok      | free        | yes         |
| Coqui TTS        | local | ok      | free        | yes         |
| edge-tts (Azure) | API   | high    | free quota  | no          |

For self-hosted: Kokoro is the current best quality/size tradeoff.
For managed: OpenAI TTS (`tts-1`, `tts-1-hd`) integrates trivially.

### ttsd daemon

Thin HTTP wrapper around TTS backend, same pattern as Whisper:

```
POST /synthesize
Content-Type: application/json
{"text": "...", "voice": "alloy", "format": "ogg_opus"}

→ audio/ogg binary
```

`VOICE_SYNTHESIS_BASE_URL` env var, similar to `WHISPER_BASE_URL`.

### Gateway integration

Router or gateway converts agent text reply to audio before sending.
Trigger options (open question below):

- Per-route flag: `voice_reply: true` in routes table
- Per-session: if input was voice → reply in voice
- Agent-controlled: `send_voice` MCP tool

### Adapter support

| Adapter | Voice output mechanism             | Format     |
| ------- | ---------------------------------- | ---------- |
| teled   | `sendVoice` (Telegram API)         | OGG Opus   |
| whapd   | `sendAudio` (WhatsApp)             | OGG Opus   |
| discd   | `sendFile` or voice channel stream | MP3 or OGG |
| emaid   | MIME attachment                    | MP3        |

Telegram voice notes require OGG/Opus specifically. MP3 works as audio
file (not voice note). Whisper outputs to the right format for Telegram.

---

## Caching

TTS is expensive (latency + cost). Cache synthesized audio by content hash:

```
groups/<folder>/media/tts/<sha256>.ogg
```

Same text + same voice = serve from cache. TTL: indefinite (content-addressed).

---

## Open questions

### Q1: What triggers voice reply?

Options:

a) **Per-conversation**: if the last user message was a voice note, reply
in voice. Simple and natural. Problem: agent always uses the same voice,
can't adapt.

b) **Per-route**: `voice_reply: true` in routes table for that JID.
Operator controls. Problem: requires schema change + dashboard support.

c) **Agent-controlled via MCP tool**: agent calls `send_voice(text)` instead
of returning text. Agent decides when voice is appropriate. Most flexible.
Problem: agent must know to use the tool; output pipeline changes.

d) **Hybrid**: per-route default (opt-in voice mode), overridable by agent.
Probably correct long-term.

### Q2: How does the agent signal voice intent?

If agent-controlled (Q1c or Q1d): does the agent call a new MCP tool,
or does it emit a structured marker in its text output that the gateway
intercepts?

MCP tool approach:

- `send_voice(text)` — clean, explicit, doesn't affect text reply path
- Requires adapter to support receiving audio upload from gateway
- Agent can mix text and voice in same turn

Marker approach:

- Agent wraps voice segment: `<speak>this will be synthesized</speak>`
- Gateway strips it, synthesizes, sends audio + remaining text
- No new MCP tool; works with existing send path

### Q3: Voice selection

Single global voice (configurable) vs per-group/per-persona voice?

- Single voice: simplest. `TTS_VOICE=alloy` env var.
- Per-group: routes table `voice_id` column. Complex.
- Per-persona: SOUL.md includes `voice: nova` frontmatter. Agent reads it
  and passes to `send_voice(text, voice="nova")`. Elegant but requires
  agent skill update.

### Q4: Long messages

TTS APIs have limits (OpenAI: 4096 chars). Agent responses can be long.
Options:

- Truncate (bad)
- Split into multiple audio files (complex, multiple sends)
- Only synthesize short responses (< N chars), fall back to text otherwise
- Agent is responsible for keeping voice replies short (instruction in CLAUDE.md)

### Q5: Self-hosted vs managed

For minimal setup: no TTS service → text-only replies even when voice input.
For full/platform profile: `ttsd` as opt-in service (like davd).

What's the default? `TTS_ENABLED=false`, only activates when
`VOICE_SYNTHESIS_BASE_URL` is set? Or always attempt if voice input?

### Q6: Where does synthesis happen?

a) In the gateway (before `channel.Send`) — gateway fetches audio, sends file
b) In the adapter (adapter calls TTS before sending) — adapter knows channel format
c) In a sidecar (MCP tool available to agent) — agent decides

Option (a) keeps adapters simple. Option (c) is most flexible.
Option (b) is an anti-pattern (adapter shouldn't talk to TTS).

**Likely answer**: gateway or MCP sidecar. Leaning toward MCP sidecar
(`send_voice` tool) so the agent controls it, consistent with how
`send_file` works.

---

## Minimal implementation path

1. `ttsd` wrapper around Kokoro or OpenAI TTS
2. `send_voice(text)` MCP tool in ipc/ (like `send_file`)
3. Adapter support: teled `sendVoice`, whapd audio
4. CLAUDE.md instruction: "use send_voice when user sent a voice message"
5. Cache layer: content-hash in media/tts/

No schema changes. No per-route config. Agent decides when to use voice.
This is the smallest path to working voice replies.

---

## Compose integration

```bash
TTS_ENABLED=true          # enable ttsd
TTS_BASE_URL=http://ttsd:8098
TTS_VOICE=alloy           # or kokoro voice name
TTS_MODEL=tts-1           # openai: tts-1 | tts-1-hd
```

`ttsd` added to compose when `TTS_ENABLED=true`, similar to `davd`.
Profile: available in `standard` and above (not minimal/web).
