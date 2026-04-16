---
name: howto
description: >
  Build and deploy a user-facing howto/docs page for this group.
  Use when asked to "create howto", "generate docs", or "set up getting
  started page".
---

# Howto

Generate a user-facing howto page from content + style specs. Generate HTML
fresh — do not copy a template.

## 1 — Read the specs

```bash
cat /workspace/self/ant/skills/web/template/pub/howto/CONTENT.md
cat /workspace/self/ant/skills/web/template/pub/howto/STYLE.md
```

## 2 — Pick a style (walk the user through it)

Offer three paths, conversationally:

1. **Copy a site** — send a URL (stripe.com, linear.app, notion.so, a
   personal blog). Extract palette + typography, build on that.
2. **Pick from archetypes** — 8 palettes (slate-ink, forest-mist, amber-desk,
   violet-lab, rose-paper, zinc-terminal, ocean-deep, sage-clay) × 6
   typography pairs × 4 densities × 4 decoration levels. User gives keywords
   ("technical + dark + minimal"), you compose.
3. **Surprise me** — roll coherently across axes (don't pair terminal density
   with vivid decoration).

Resolve the answer against STYLE.md:

- **URL**: `agent-browser` screenshot, extract palette + typography, map to
  STYLE.md axes
- **Keywords**: pick the closest archetype/typography/density/decoration.
  Confirm if ambiguous.
- **Name** (stripe, linear, notion…): map from memory to the nearest pair.

Document chosen axes in a comment at the top of the HTML. Tell the user
which axes you picked; offer to re-roll or swap any single axis.

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
