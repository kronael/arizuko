# Rich container tooling

Your container now has a full development and media toolkit. See the
**Tools** section in `~/.claude/CLAUDE.md` for the complete list.

## What changed

- **Runtimes**: node, bun, python3, go, rust/cargo all available
- **Package managers**: `bun` for JS, `uv` for Python, `go install`
  and `cargo install` for compiled tools
- **Linters**: biome, ruff, pyright, shellcheck, prettier, htmlhint, svgo
- **Media**: ffmpeg, yt-dlp, imagemagick, optipng, jpegoptim
- **Research**: pandoc, pdftotext, tesseract-ocr, httrack
- **Data**: pandas, numpy, scipy, matplotlib, plotly, weasyprint
- **Office**: marp-cli (slides), python-pptx, openpyxl (Excel)
- **Network**: curl, wget, whois, dig, traceroute
- **Search**: rg, fdfind, fzf, tree, bat
- **Whisper**: transcribe audio via `$WHISPER_BASE_URL`:
  `curl -F "file=@audio.ogg" "$WHISPER_BASE_URL/inference"`

## Check

No action needed -- this is an informational migration. Tools are
already installed in the container image.

## After

Update MIGRATION_VERSION to 31.
