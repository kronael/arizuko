# 156 — creative skills (hermes-derived)

Eight new stock skills, adapted from NousResearch/hermes-agent
`skills/creative/` (MIT). Pure creative capability — no daemon, MCP,
route, or schema change. Discover via `/dispatch` on the keywords below;
all are `user-invocable`.

- `ascii-art` — text banners (pyfiglet), cowsay, boxes, image-to-ASCII,
  QR codes (local `qrencode`), Unicode fallback. "ASCII art", "banner".
- `ascii-video` — ASCII-art MP4/GIF from video/audio/image/generative
  input via ffmpeg + numpy. "ASCII video", "audio visualizer".
- `excalidraw` — hand-drawn diagrams as `.excalidraw` JSON; deliver via
  `send_file`, open at excalidraw.com. "draw a diagram", "flowchart".
- `manim-video` — 3Blue1Brown-style math/technical animations (Manim CE).
  Install Manim per-turn (`uv pip install --system manim`); LaTeX is NOT
  in the image — use `Text`/`MarkupText` for labels. "math animation".
- `p5js` — generative/interactive browser art; self-contained HTML (CDN
  p5.js), headless chromium export, publish to `~/public_html/`.
  "generative art", "creative coding", "shader".
- `popular-web-designs` — 54 real-site design systems (Stripe, Linear,
  Vercel, …); generate matching HTML/CSS, serve from your web slot.
  "make it look like stripe", "landing page".
- `songwriting-and-ai-music` — songwriting craft + Suno prompt
  engineering, parody/adaptation, phonetic tricks. "write a song".
- `ideation` — constraint-driven project-idea generation. "give me a
  project idea", "I'm bored", "what should I make".

Runtime adapted to arizuko: Bash (not hermes `terminal`), `Write`/`Read`
(not `write_file`/`skill_view`), `uv`/`uvx` (not bare pip), `~/public_html`
web slots + `send_file` for delivery, `agent-browser` for screenshots,
crackbox egress allowlist for any remote API/CDN. Existing skills, MCP
tools, routes, REST endpoints unchanged.
