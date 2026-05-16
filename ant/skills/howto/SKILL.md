---
name: howto
description: >
  Build and deploy the product-branded user-facing howto page under
  `/workspace/web/pub/howto/`. Reads `~/BRANDING.md` for product name,
  tagline, voice notes, accent colour, channels, and setup link. USE
  for "create howto", "generate docs", "set up getting started page",
  "show me my web", landing page, onboarding page. NOT for general web
  apps (use web) or knowledge hubs (use hub).
user-invocable: true
---

# Howto

Generate ONE user-facing howto page for this deployment's product. The
page wears the product's brand — name, tagline, voice, accent colour —
not a neutral arizuko default. Generate HTML fresh; never copy a
template.

## 1 — Read the branding guide

```bash
cat ~/BRANDING.md  # may be absent on legacy installs
```

`BRANDING.md` is YAML frontmatter + Markdown body:

```
---
name: Rhias                                  # canonical user-facing name
slug: rhias                                  # template / route slug
tagline: One sentence the page leads with.
accent: "#2f5d3a"                            # CSS var override for --accent
channels:
  primary: Telegram
  alternative: WhatsApp                      # optional
setup_url: /pub/products/<slug>/setup.html
---

# Voice notes
- 3-5 bullets on register (terse vs warm, formal vs casual, avoid-list).

# What you get
- 4-6 bullets framed for the END USER.

# Sample exchange
```
you    ...
bot    ...
```
```

If `~/BRANDING.md` is absent: fall back to the generic template. Read
`~/SOUL.md` if present for a tagline + persona snippet (legacy path),
otherwise render the template as-is. Do not invoke `/soul`.

## 2 — Read the content + style specs

```bash
cat /workspace/self/ant/skills/web/template/pub/howto/CONTENT.md
cat /workspace/self/ant/skills/web/template/pub/howto/STYLE.md
```

## 3 — Pick a style

Three paths, ask conversationally:

| Path | Input | How to resolve |
| ---- | ----- | -------------- |
| Copy a site | URL | `agent-browser` screenshot → extract palette + typography → map to STYLE.md axes |
| Archetypes | Keywords ("technical + dark + minimal") | pick from STYLE.md axes; confirm if ambiguous |
| Surprise me | none | Roll coherently (don't pair terminal density with vivid decoration) |

When `BRANDING.md` provides `accent:`, override the chosen archetype's
accent with that hex. The rest of the palette derives from the
archetype as usual. Document chosen axes in an HTML comment at the
top of the file.

## 4 — Generate the HTML

Page structure mirrors `template/web/pub/products/<slug>/index.html`:

- breadcrumb (`arizuko › <product name>`)
- hero: `<h1>` is the product `name`; lede is the `tagline`
- 4-6 "what you get" bullets — verbatim from BRANDING.md body
- sample exchange block — verbatim from BRANDING.md body
- channels line — primary + alternative
- the CONTENT.md sections (commands, topics, files, …) follow as
  reference material
- footer link to `setup_url` for operators
- dark-mode toggle (fixed, top-right), mobile-responsive
- inline `<style>` override: `:root { --accent: <accent>; }` when
  BRANDING.md supplied one

Voice steering: when filling the prose under each CONTENT.md section,
write in the register the BRANDING.md voice notes prescribe. A warm,
curious brand produces warm, curious prose. A terse, professional
brand produces terse, professional prose. Voice notes go in as a
steering instruction; never paste them visibly into the page.

Substitutions in CONTENT.md still apply:

- `$ASSISTANT_NAME agent` → BRANDING.md `name` (or `$ARIZUKO_ASSISTANT_NAME`)
- `bot.example.com` → `$WEB_HOST`
- `{{TAGLINE}}` → BRANDING.md `tagline`
- `{{BOT_NAME}}` → `$ARIZUKO_GROUP_NAME`
- `{{WORLD}}` → `$ARIZUKO_WORLD`

## 5 — Write and verify

```bash
WEB_DIR="/workspace/web/pub"
mkdir -p "$WEB_DIR/howto"
# write HTML to $WEB_DIR/howto/index.html
curl -sL -o /dev/null -w '%{http_code}' "https://$WEB_HOST/pub/howto/"
```

Report URL, product name, chosen style axes.

## Rules

- NEVER use a pre-written HTML template — always generate fresh
- Output path is always `/workspace/web/pub/howto/index.html` — one
  howto per deployment, matching the one primary product
- Footer MUST read: `powered by <a href="https://arizuko.example/arizuko">arizuko</a>`
- NEVER attribute to Anthropic or Claude
- BRANDING.md is operator-authored canonical truth — never edit it
  from this skill
