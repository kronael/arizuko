# 031 — rich container tooling

Container image now ships with a full dev + media toolkit. See the
**Tools** section in `~/.claude/CLAUDE.md` for the full list.

- Runtimes: node, bun, python3, go, rust/cargo
- Package managers: bun, uv, `go install`, `cargo install`
- Linters: biome, ruff, pyright, shellcheck, prettier, htmlhint, svgo
- Media: ffmpeg, yt-dlp, imagemagick, optipng, jpegoptim
- Research: pandoc, pdftotext, tesseract-ocr, httrack
- Data: pandas, numpy, scipy, matplotlib, plotly, weasyprint
- Office: marp-cli, python-pptx, openpyxl
- Network: curl, wget, whois, dig, traceroute
- Search: rg, fdfind, fzf, tree, bat
- Whisper: `curl -F "file=@audio.ogg" "$WHISPER_BASE_URL/inference"`
