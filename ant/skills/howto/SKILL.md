---
name: howto
description: Build and deploy a user-facing howto/docs page for this group.
when_to_use: Use when asked to "create howto", "generate docs", or "set up getting started page".
---

# Howto

Generate a user-facing howto page from content + style specs. Generate HTML
fresh — do not copy a template.

## 1 — Read the specs

```bash
cat /workspace/self/ant/skills/web/template/pub/howto/CONTENT.md
cat /workspace/self/ant/skills/web/template/pub/howto/STYLE.md
```

## 1a — Read SOUL.md (if present)

If `~/SOUL.md` exists, read it and extract a one-line tagline (from the
Voice section) plus a short persona note (from Persona/Quirks). Inject:

- tagline as a line under the page h1
- first-person rewrite of section 12's intro using `$ARIZUKO_GROUP_NAME`
  (as `{{BOT_NAME}}`) and `$ARIZUKO_WORLD` (as `{{WORLD}}`)

If `~/SOUL.md` is absent: render the template as-is. Do not invoke
`/soul` — that skill is user-initiated only.

## 2 — Pick a style

Ask conversationally; three paths:

| Path | Input | How to resolve |
| ---- | ----- | -------------- |
| Copy a site | URL | `agent-browser` screenshot → extract palette + typography → map to STYLE.md axes |
| Archetypes | Keywords ("technical + dark + minimal") | 8 palettes × 6 typography × 4 densities × 4 decorations; pick closest, confirm if ambiguous |
| Surprise me | none | Roll coherently (don't pair terminal density with vivid decoration) |

Document chosen axes in a comment at the top of the HTML. Tell the user which axes; offer to re-roll or swap any axis.

## 3 — Generate the HTML

From CONTENT.md, in the chosen style. Self-contained file with:

- TLDR grid at top (one card per section)
- All sections as full cards with prose + code blocks
- `$ASSISTANT_NAME agent` → `$ARIZUKO_ASSISTANT_NAME` in title and h1
- `bot.example.com` → `$WEB_HOST`
- Dark-mode toggle (fixed, top-right)
- Mobile-responsive

## 4 — Write and verify

```bash
if [ "$ARIZUKO_IS_ROOT" = "1" ]; then
  WEB_DIR="/workspace/web/pub"
else
  WEB_DIR="/workspace/web/pub/$(basename "$HOME")"
fi
mkdir -p "$WEB_DIR/howto"
# write HTML to $WEB_DIR/howto/index.html
curl -sL -o /dev/null -w '%{http_code}' "https://$WEB_HOST/pub/howto/"
```

Report the URL and chosen style to the user.

## Rules

- NEVER use a pre-written HTML template — always generate fresh
- Footer MUST read: `powered by <a href="https://arizuko.example/arizuko">arizuko</a>`
- NEVER attribute to Anthropic or Claude
