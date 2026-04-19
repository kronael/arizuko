---
status: shipped
---

# MIME Pipeline

Media attachment processing. Runs on every inbound message before
container spawn — downloads, transcribes, annotates. Code:
`gateway/enrich_*.go`.

## Enricher pipeline model

```
InboundMessage -> [Enricher Pipeline] matches? enrich()
  -> EnrichedMessage (annotated) -> ContainerInput -> Container
```

Enrichers run in parallel. Failures logged and skipped.

## ContextAnnotation

Enrichers produce `ContextAnnotation { label, content, order }`.
Order determines position in prompt assembly.

## Prompt assembly format

```xml
<attachment index="0" type="voice" path="/home/node/media/...">
  <transcript>...</transcript>
</attachment>
<attachment index="1" type="image" path="/home/node/media/...">
  <description>file saved</description>
</attachment>

hey check this out
```

## Built-in enrichers

- **VoiceTranscriber** (order 10): voice/audio -> whisper -> transcription
- **VideoAudioTranscriber** (order 11): video -> ffmpeg extract audio -> whisper

## Config env vars

```
MEDIA_ENABLED=true
MEDIA_MAX_FILE_BYTES=20971520        # 20 MB
VOICE_TRANSCRIPTION_ENABLED=true
WHISPER_BASE_URL=http://localhost:8080
WHISPER_MODEL=turbo
VIDEO_TRANSCRIPTION_ENABLED=false    # requires ffmpeg
```

## Media file layout

```
groups/<folder>/media/<YYYYMMDD>/
  <msg-id>-<idx>.<ext>            -- raw download
  <msg-id>-<idx>-<enricher>.txt   -- enricher output
```
