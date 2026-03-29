# 051 — Inbound media attachments

The gateway now downloads photos, documents, and voice messages before running
the agent. Attachment paths are injected as XML into message content.

## What changed

- `CLAUDE.md` has a new "Inbound media attachments" section
- `<attachment path="..." mime="..." filename="..."/>` elements appear in message content
- Voice messages include a `transcript="..."` attribute when whisper is configured
- Files saved under `~/media/<YYYYMMDD>/` (gitignored, transient)

## Agent action required

None — attachments appear automatically in your message context. Read the path
with file tools to process images/docs, or use `transcript` for pre-transcribed voice.
