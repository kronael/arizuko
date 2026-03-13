---
status: shipped
---

# MIME Pipeline

Media attachment processing. Runs on every inbound message
before container spawn — downloads, transcribes, annotates.

## Model

```
InboundMessage (from channel)
  -> [Enricher Pipeline] matches(msg)? enrich(msg, ctx)
  -> EnrichedMessage (annotated, attachments resolved)
  -> ContainerInput (stdin JSON)
  -> Container (Claude agent)
```

Enrichers run in parallel. Failures logged and skipped.

## Interfaces

```typescript
interface InboundMessage {
  id: string;
  chatJid: string;
  sender: string;
  senderName: string;
  text: string;
  timestamp: string;
  channel: 'telegram' | 'whatsapp' | 'discord';
  groupFolder: string;
  isMain: boolean; // enricher-only context (not in ContainerInput)
  attachments?: RawAttachment[];
  replyToText?: string;
  replyToSender?: string;
  threadId?: string;
  mediaGroupId?: string;
}

interface RawAttachment {
  type: 'image' | 'voice' | 'audio' | 'video' | 'document' | 'sticker';
  mimeType?: string;
  filename?: string;
  sizeBytes?: number;
  durationSeconds?: number;
  source: TelegramSource | WhatsAppSource | DiscordSource;
}

interface EnrichedAttachment {
  type: RawAttachment['type'];
  localPath: string;
  filename: string;
  mimeType?: string;
  sizeBytes: number;
  transcription?: string;
  annotation?: string;
}

interface EnrichContext {
  groupDir: string;
  mediaDir: string;
  config: EnricherConfig;
  channel: Channel;
}

interface EnrichedMessage extends InboundMessage {
  enrichedAttachments?: EnrichedAttachment[];
  contextAnnotations: ContextAnnotation[];
}

interface ContextAnnotation {
  label: string;
  content: string;
  order: number;
}

interface MessageEnricher {
  name: string;
  matches(msg: InboundMessage): boolean;
  enrich(msg: EnrichedMessage, ctx: EnrichContext): Promise<EnrichedMessage>;
}
```

## Built-in enrichers

### VoiceTranscriber

```
matches:  type === 'voice'|'audio' or audio/* mime
enriches: download -> media/voice/<id>.ogg -> whisper
          -> transcription + ContextAnnotation(order:10)
config:   VOICE_TRANSCRIPTION_ENABLED, WHISPER_BASE_URL,
          WHISPER_MODEL
```

### VideoAudioTranscriber

```
matches:  type === 'video' or video/* mime
enriches: download -> media/video/<id>.mp4
          -> ffmpeg extract audio -> whisper
          -> transcription + ContextAnnotation(order:11)
config:   VIDEO_TRANSCRIPTION_ENABLED, WHISPER_BASE_URL
requires: ffmpeg in PATH
```

### GenericFileSaver

Covered by the pipeline's default behavior: all attachments are
saved and produce `[media attached: ...]` lines. No separate
handler needed.

## Prompt assembly

```xml
<attachment index="0" type="voice" path="/home/node/media/...">
  <transcript>...</transcript>
</attachment>
<attachment index="1" type="image" path="/home/node/media/...">
  <description>file saved</description>
</attachment>

hey check this out
```

Enricher output files: `-whisper.txt` / `-<enricher>.txt`.

## Config

```bash
MEDIA_ENABLED=true
MEDIA_MAX_FILE_BYTES=20971520        # 20 MB
VOICE_TRANSCRIPTION_ENABLED=true
WHISPER_BASE_URL=http://localhost:8080
WHISPER_MODEL=turbo
VIDEO_TRANSCRIPTION_ENABLED=false    # requires ffmpeg
```

## File layout

```
src/
  mime-enricher.ts        -- interfaces, runEnrichers()
  mime-handlers/
    voice.ts              -- VoiceTranscriber
    video.ts              -- VideoAudioTranscriber
    whisper.ts            -- Whisper API client
  channels/               -- extractAttachments() per channel
  container-runner.ts     -- mount /workspace/media
```

## Container mount

```
groups/<folder>/media/ -> /workspace/media/
  <YYYYMMDD>/
    <msg-id>-<idx>.<ext>            -- raw download
    <msg-id>-<idx>-<enricher>.txt   -- enricher output
```

## Extension

1. Implement handler in `src/mime-handlers/<name>.ts`
2. Add `<NAME>_ENABLED` to `config.ts`
3. Register in `src/mime-enricher.ts`
