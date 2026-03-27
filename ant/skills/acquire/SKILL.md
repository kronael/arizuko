---
name: acquire
description: >
  Download video/audio (yt-dlp), transcribe (whisper), scrape
  web pages. Use when gathering source material for research
  or analysis.
---

# Data Acquisition

Low-level download, transcription, and scraping patterns.

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

Supported sites: YouTube, Twitter/X, Reddit, TikTok, Vimeo, 1000+ others.
Run `yt-dlp --list-extractors` to check.

## Audio transcription (whisper)

Transcribe via `$WHISPER_BASE_URL`:

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

Claude reads images natively via Read tool:

```bash
curl -o ~/tmp/img.jpg '<url>'
```

Then `Read("~/tmp/img.jpg")` — works for photos, charts, screenshots, PDFs.

## Web content

- Static: `curl -s '<url>'`
- JS-rendered / auth-required: use `agent-browser` skill

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

## Rules

- ALWAYS prefer transcripts over raw media — text is cheaper to process
- ALWAYS save intermediate files to `~/tmp/` (sendable via `send_file`)
- ALWAYS run `yt-dlp --dump-json` before downloading — description/comments often suffice
- NEVER download full video when subtitles/transcript are available
- Batch when possible — yt-dlp accepts playlists and multiple URLs
