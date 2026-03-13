---
status: spec
---

# Agent Capabilities

Agent containers are rich environments with multimedia
acquisition, compilation toolchains, and browser automation.

## Container tooling

**Already in container**: node, bun, chromium, agent-browser,
curl, git, claude-code

**Adding**: ffmpeg, yt-dlp, python3, jq, build-essential,
go, rust (cargo), wget

This lets agents download video, transcribe audio, compile
tools from source, and install packages from the internet.

## Gateway → agent data flow

| Media   | Gateway processing  | What agent sees                |
| ------- | ------------------- | ------------------------------ |
| Voice   | whisper transcribes | `[voice/auto→en: text]`        |
| Video   | ffmpeg → whisper    | `[video audio: text]`          |
| Image   | passed through      | attachment path (vision reads) |
| PDF/doc | passed through      | attachment path (Read tool)    |

## Agent → whisper (direct)

`WHISPER_BASE_URL` env var passed to container. Agent can
transcribe audio it downloads (yt-dlp output, podcast
episodes, etc.) without going through gateway IPC.

## Skill: acquire

`container/skills/acquire/SKILL.md` — teaches agents the
multimedia acquisition strategy:

1. Prefer transcripts over raw media
2. Screenshot key moments for visual context
3. Metadata first (often enough without full download)
4. Non-obvious search services (DeepWiki, Marginalia, etc.)

## Content presentation

Decision: short → chat message, rich → web page,
raw data → send_file. Documented in howto skill.

## Open

- Container image size impact (go+rust+chromium+ffmpeg is heavy)
