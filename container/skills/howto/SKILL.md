---
name: howto
description: >
  Build and deploy a user-facing howto/docs page for this group.
  Use when asked to "create howto", "generate docs", or "set up getting
  started page".
---

# Howto

User-facing howto page: 20 topics, TLDR grid at top, deep-dive sections below.

## Step 1 — Ask for a theme

Before building, ask:

> "Which site's style should I imitate? Give me a URL or name (stripe.com,
> linear.app, notion.so…). Or say 'default' for the warm earth-tone theme."

If given a URL: use `agent-browser` to open and screenshot it, extract the
design system (colors, fonts, radius, card style, code blocks), then restyle
the template to match. Keep structure, replace visuals.

## Step 2 — Build

```bash
# resolve web dir
if [ "$NANOCLAW_IS_ROOT" = "1" ] || [ "$NANOCLAW_IS_WORLD_ADMIN" = "1" ]; then
  WEB_DIR="/workspace/web"
else
  WEB_DIR="/workspace/web/$(basename $NANOCLAW_GROUP_FOLDER)"
fi
mkdir -p "$WEB_DIR/howto"
cp /workspace/self/container/skills/web/template/pub/howto/index.html \
   "$WEB_DIR/howto/index.html"
```

Then customize the copy:

- Replace `kanipi agent` in `<title>` and `<h1>` with `$ASSISTANT_NAME`
- Replace `bot.example.com` with `$WEB_HOST` (NEVER guess if empty)
- Remove sections for unconfigured features (voice if no voice channel, onboarding if not enabled)
- Apply chosen theme (or leave default)

## Step 3 — Verify and link

```bash
[ -f "$WEB_DIR/index.html" ] || echo '<a href="howto/">Getting Started →</a>' > "$WEB_DIR/index.html"
curl -sL -o /dev/null -w '%{http_code}' "https://$WEB_HOST/$NANOCLAW_GROUP_FOLDER/howto/"
```

Tell the user the URL.

## Rules

- Footer MUST read: `powered by <a href="https://REDACTED/arizuko">arizuko</a>`
- NEVER attribute to Anthropic or Claude
- NEVER rebuild from scratch — the template has all 20 sections pre-written
