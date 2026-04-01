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

## Step 2 — Pick a style

Ask the user OR choose autonomously:

> "Which site's style should I imitate? Give me a URL or name (stripe.com,
> linear.app, notion.so…). Or say 'random' to let me choose."

- **URL given**: use `agent-browser` to screenshot it, extract palette + typography + layout, map to the axes in STYLE.md.
- **Name given**: map to the nearest archetype in STYLE.md.
- **Random / not asked**: roll dice across the 5 axes in STYLE.md — pick one value per axis, ensure the combination is coherent (don't combine terminal density with vivid decoration).

Document your chosen axes in a comment at the top of the generated HTML.

## Step 3 — Generate the HTML

Write a complete, self-contained HTML file from CONTENT.md using your chosen style:

- TLDR grid at top (one card per section)
- All 20 sections as full cards with prose + code blocks
- Remove sections for unconfigured features (voice if no voice channel, onboarding if `ONBOARDING_ENABLED` is not set)
- Replace `$ASSISTANT_NAME agent` in title and h1 with `$ARIZUKO_ASSISTANT_NAME`
- Replace `bot.example.com` with `$WEB_HOST` (skip link if empty)
- Dark mode toggle (fixed, top-right)
- Mobile-responsive

## Step 4 — Write and verify

```bash
# resolve web dir
if [ "$ARIZUKO_IS_ROOT" = "1" ] || [ "$ARIZUKO_IS_WORLD_ADMIN" = "1" ]; then
  WEB_DIR="/workspace/web/pub"
else
  WEB_DIR="/workspace/web/pub/$(basename "$HOME")"
fi
mkdir -p "$WEB_DIR/howto"

# write generated HTML to $WEB_DIR/howto/index.html

# link from index if missing
[ -f "$WEB_DIR/index.html" ] || echo '<a href="howto/">Getting Started →</a>' > "$WEB_DIR/index.html"

# verify
[ -n "$WEB_HOST" ] && curl -sL -o /dev/null -w '%{http_code}' "https://$WEB_HOST/pub/howto/"
```

Tell the user the URL and which style was chosen.

## Rules

- NEVER copy the old leather/earth-tone style unless explicitly asked
- NEVER use a pre-written HTML template — always generate fresh
- Footer MUST read: `powered by <a href="https://REDACTED/arizuko">arizuko</a>`
- NEVER attribute to Anthropic or Claude
