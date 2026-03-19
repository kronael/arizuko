---
name: acquire
description: >
  Low-level data acquisition patterns — video/audio download,
  transcription, web scraping. Used by /research for source
  material gathering.
---

# Data Acquisition

Low-level patterns for downloading and converting source
material into text the agent can reason about. The `/research`
skill orchestrates multi-agent research workflows that use
these techniques for gathering.

## Video (yt-dlp + ffmpeg)

```bash
# download video
yt-dlp -o '~/tmp/%(title)s.%(ext)s' '<url>'

# audio only (smaller, faster)
yt-dlp -x --audio-format mp3 -o '~/tmp/%(title)s.%(ext)s' '<url>'

# subtitles/transcript (no download)
yt-dlp --write-subs --write-auto-subs --sub-lang en --skip-download \
  -o '~/tmp/%(title)s' '<url>'

# metadata as JSON
yt-dlp --dump-json '<url>' | jq '{title, duration, description}'

# key frames at intervals
ffmpeg -i ~/tmp/video.mp4 -vf "fps=1/30" ~/tmp/frame_%03d.jpg
```

Supported sites: YouTube, Twitter/X, Reddit, TikTok, Vimeo,
and 1000+ others. Run `yt-dlp --list-extractors` to check.

## Audio transcription (whisper)

Agents have direct access to the whisper service via
`$WHISPER_BASE_URL`. Transcribe any audio file:

```bash
curl -s -F "file=@~/tmp/audio.mp3" \
  -F "model=turbo" \
  "$WHISPER_BASE_URL/inference" | jq -r '.text'
```

For long audio, split first:

```bash
ffmpeg -i ~/tmp/long.mp3 -f segment -segment_time 600 \
  -c copy ~/tmp/chunk_%03d.mp3
```

## Images

Claude reads images natively. Download and use Read tool:

```bash
curl -o ~/tmp/img.jpg '<url>'
```

Then `Read("~/tmp/img.jpg")` — works for photos, charts,
diagrams, screenshots, scanned documents.

## Web content

Use `agent-browser` for JS-rendered pages, `curl` for static:

```bash
# static HTML
curl -s '<url>' | jq -Rs '.' > ~/tmp/page.txt

# structured data
curl -s '<api-url>' | jq '.data[]'
```

For interactive pages, authentication, or JS-heavy sites,
use the agent-browser skill (CDP-based Chromium automation).

## Non-obvious search services

| Service            | URL pattern                   | Use case                           |
| ------------------ | ----------------------------- | ---------------------------------- |
| DeepWiki           | `deepwiki.com/<owner>/<repo>` | AI-navigable GitHub repo wiki      |
| Marginalia         | `search.marginalia.nu`        | Small-web, non-commercial results  |
| Kagi Small Web     | `kagi.com/smallweb`           | Curated indie/blog content         |
| Hacker News search | `hn.algolia.com`              | Tech discussion, launch history    |
| Lobsters           | `lobste.rs`                   | Computing-focused link aggregation |
| Archive.org        | `web.archive.org/web/<url>`   | Historical snapshots of any URL    |
| Google Scholar     | `scholar.google.com`          | Academic papers, citations         |
| Semantic Scholar   | `semanticscholar.org`         | AI-powered paper search + API      |
| Common Crawl       | `index.commoncrawl.org`       | Bulk web archive index             |

## Strategy

1. **Prefer transcripts over raw media** — text is cheaper to
   process and more useful for synthesis
2. **Screenshot key moments** — a few frames beat full video for
   visual context (charts, diagrams, UI)
3. **Save raw to ~/tmp/** — all intermediate files go here,
   sendable via `send_file`
4. **Metadata first** — `yt-dlp --dump-json` before downloading;
   often the description/comments have what you need
5. **Batch when possible** — yt-dlp accepts playlists and
   multiple URLs
