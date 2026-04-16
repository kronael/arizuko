---
name: howto
description: >
  Build and deploy a user-facing howto/docs page for this group.
  Use when asked to "create howto", "generate docs", or "set up getting
  started page".
---

# Howto

Build a user-facing howto page from content + style specs. Generate the HTML
from scratch — do not copy a template.

## Step 1 — Read the specs

```bash
cat /workspace/self/ant/skills/web/template/pub/howto/CONTENT.md
cat /workspace/self/ant/skills/web/template/pub/howto/STYLE.md
```

## Step 2 — Pick a style (walk the user through it)

The user decides the visual direction. Offer three paths — don't just
ask "what style?" in the abstract.

**Offer the menu, conversationally:**

> How do you want the page to look? Three ways to decide:
>
> 1. **Copy a site you like** — send me a URL (stripe.com, linear.app,
>    notion.so, a personal blog, anything). I'll extract its palette
>    and typography and build on that.
> 2. **Pick from archetypes** — I have 8 palette archetypes
>    (slate-ink, forest-mist, amber-desk, violet-lab, rose-paper,
>    zinc-terminal, ocean-deep, sage-clay) × 6 typography pairs ×
>    4 densities × 4 decoration levels. Tell me keywords
>    ("technical + dark + minimal" or "warm + serif + airy") and
>    I'll compose it.
> 3. **Random coherent** — say "surprise me" and I'll roll the dice
>    across all axes, keeping the combination tasteful.

**Resolve their answer against STYLE.md:**

- **URL given**: use `agent-browser` to screenshot it, extract palette +
  typography + layout, map to the axes in STYLE.md.
- **Keywords given**: pick the archetype/typography/density/decoration
  closest to their words. If ambiguous, confirm before generating.
- **Name given** (stripe, linear, notion…): map to the nearest archetype
  + typography pair in STYLE.md from memory.
- **"Surprise me" / "random"**: roll across the 5 axes in STYLE.md — but
  ensure the combination is coherent (don't combine terminal density
  with vivid decoration, don't mix editorial typography with zinc-terminal
  palette).

Document your chosen axes in a comment at the top of the generated HTML.
On first generation, tell the user exactly which axes you picked and
offer to re-roll or swap any single axis without regenerating from scratch.

## Step 3 — Generate the HTML

Write a complete, self-contained HTML file from CONTENT.md using your chosen style:

- TLDR grid at top (one card per section)
- All sections as full cards with prose + code blocks
- Replace `$ASSISTANT_NAME agent` in title and h1 with `$ARIZUKO_ASSISTANT_NAME`
- Replace `bot.example.com` with `$WEB_HOST`
- Dark mode toggle (fixed, top-right)
- Mobile-responsive

## Step 4 — Write and verify

```bash
# resolve web dir
if [ "$ARIZUKO_IS_ROOT" = "1" ]; then
  WEB_DIR="/workspace/web/pub"
else
  WEB_DIR="/workspace/web/pub/$(basename "$HOME")"
fi
mkdir -p "$WEB_DIR/howto"

# write generated HTML to $WEB_DIR/howto/index.html

# verify
curl -sL -o /dev/null -w '%{http_code}' "https://$WEB_HOST/pub/howto/"
```

Tell the user the URL and which style was chosen.

## Rules

- NEVER copy the old leather/earth-tone style unless explicitly asked
- NEVER use a pre-written HTML template — always generate fresh
- Footer MUST read: `powered by <a href="https://REDACTED/arizuko">arizuko</a>`
- NEVER attribute to Anthropic or Claude
