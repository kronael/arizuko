# arizuko doc site

Static HTML served at `/pub/` by vited. Source lives in
`template/web/pub/` in the arizuko repo; it is copied verbatim
into each instance's `<data-dir>/web/pub/` on `arizuko create`.
No build step — plain HTML + one CSS file + one JS file.

## Deploy to live krons (operational procedure)

The canonical command in root CLAUDE.md is
`sudo rsync -a --delete template/web/pub/ /srv/data/arizuko_krons/web/pub/arizuko/`.
The local permission policy blocks `rsync`, and the live target
contains files that must not be deleted, so the actual workflow is:

```bash
# 1. Copy contents in (no sudo — target is owned by onvos)
cp -r template/web/pub/. /srv/data/arizuko_krons/web/pub/arizuko/

# 2. Diff to find stale files left over (cp doesn't delete)
(cd template/web/pub && find . -type f | sort > /tmp/src.list)
(cd /srv/data/arizuko_krons/web/pub/arizuko && find . -type f | sort > /tmp/dst.list)
diff /tmp/src.list /tmp/dst.list | grep '^>' | head -40

# 3. Delete only verified-stale doc subtrees (sudo — files owned by mivu/root).
#    PRESERVE: ./CLAUDE.md, ./.nomigrate, ./*.bak-krons, ./skills-export/**
#    DELETE candidates: ./cookbooks/, ./docs/, ./blog/, ./*.bak-<date>-style
sudo rm -rf /srv/data/arizuko_krons/web/pub/arizuko/<stale-dirs...>
sudo find /srv/data/arizuko_krons/web/pub/arizuko -name '*.bak-*-style' -delete

# 4. Verify live
for p in / /components/ /concepts/ /reference/ /howto/ /products/; do
  code=$(curl -s -o /dev/null -w "%{http_code}" "https://krons.fiu.wtf/pub/arizuko$p")
  printf "%-30s %s\n" "$p" "$code"
done
```

Hard rules:

- Touch ONLY `/srv/data/arizuko_krons/web/pub/arizuko/`. Siblings
  (`agents/`, `arts/`, `auto/`, `arizuko-landing.html`, …) belong
  to other sites — never write or delete there.
- No `--delete`-style flag on the copy. Always copy first, diff,
  then targeted delete with an explicit list.
- Preserve `CLAUDE.md`, `.nomigrate`, `*.bak-krons`, `skills-export/`
  on the live target — instance state and user data, not docs.
- Don't deploy uncommitted source. If `git status template/web/pub/`
  is dirty, commit first.

## Layout

```
pub/
  index.html              landing — one-pager: what arizuko is, quick start
  products/
    index.html            product grid — what can I build?
    assistant/
      index.html          product intro (user pitch, key features, who it's for)
      setup.html          setup guide: arizuko commands, env vars, first run
    developer/index.html + setup.html
    researcher/index.html + setup.html
    writer/index.html + setup.html
    ops/index.html + setup.html
    support/index.html + setup.html
    companion/index.html + setup.html
  components/
    index.html            all daemons, one-line each, links
    gated.html            gateway daemon — what, why, how it fits, standalone
    ant.html              agent-as-folder unit (planned move from concepts/)
    slink.html            public web endpoint (planned move from concepts/)
    proxyd.html           auth proxy
    webd.html             web channel + SSE hub
    crackbox.html         egress sandbox (planned move from crackbox/)
    onbod.html            onboarding daemon
    dashd.html            operator dashboard
    timed.html            cron/interval scheduler
    channels.html         adapter overview (teled, discd, whapd, mastd, bskyd, reditd, emaid, twitd, linkd)
    davd.html             WebDAV workspace
    ttsd.html             TTS proxy
  reference/
    index.html            reference overview
    env.html              all env vars, grouped by daemon
    cli.html              arizuko CLI commands (create, run, invite, chat, group …)
    mcp.html              MCP tools reference (all 30+ tools, params, tiers)
    schema.html           SQLite tables (messages, groups, user_groups, …)
    grants.html           grant rule syntax (planned move from concepts/)
    jid.html              JID format reference (planned move from concepts/)
  howto/
    index.html            getting started — install, create instance, first message
  concepts/               kept for external links; thin wrappers pointing to /components/ or /reference/
  changelog/index.html
  assets/
    hub.css               single stylesheet (dark/light, prose, code, nav)
    hub.js                theme toggle + minimal nav
```

## Section purposes

### Landing (`index.html`)

Audience: someone who just heard of arizuko. Answer in order:

1. What is it? (one sentence)
2. What do you get? (5–8 bullets, concrete)
3. Show me (CLI snippet: create → run → talk)
4. Go deeper (links to products/, components/, reference/, howto/)

No architecture diagrams on the landing. Keep it under 300 lines of HTML.

### Products (`products/`)

Audience: operator deciding what to deploy. Replaces "cookbooks."

`products/index.html` — product grid. Each product: name, tagline,
best channel, 2-line description. Links to `products/<name>/`.

`products/<name>/index.html` — user-facing intro:

- What this agent does (from the user's perspective, not the operator's)
- Best channels (where it lives)
- Sample interaction (2-3 message exchange)
- Who it's for (individual / team / customer-facing)
- Link to setup.html

`products/<name>/setup.html` — operator setup:

- Prerequisites (which adapters, features needed)
- Exact commands (`arizuko create`, env vars to set)
- Knowledge base / facts/ population (if relevant)
- Verification (how to know it's working)
- Tuning (PERSONA.md customisation, skill enable/disable)

One product = two pages. The intro sells it to the end user; setup
is for the operator. Keep them separate — operators share setup.html
links in docs; users see index.html.

### Components (`components/`)

Audience: operator evaluating a component or integrating it elsewhere.

Each component page answers four questions in order:

1. **What is it?** One sentence. Not marketing — mechanism.
2. **Why does it exist?** The system-design reason. What would break without it.
3. **How does it fit?** Callout box: inputs → daemon → outputs. Deps.
4. **Standalone usage.** How to run it outside arizuko (config, ports, auth).
   If it can't run standalone, say so and explain why.

`components/index.html` — table of all daemons: name | kind (core/integration)
| one-line role | link. Same data as README.md daemon table — keep in sync.

Component pages are ~200–400 lines of HTML. Link to reference/env.html
anchored to that daemon's section.

### Reference (`reference/`)

Audience: operator looking up a specific config value, CLI flag, MCP tool,
or schema column. Comprehensive, flat, searchable.

`reference/env.html` — every env var documented:

- Daemon that reads it
- Type (string / int / bool / URL)
- Default
- Effect
- Example

`reference/cli.html` — every `arizuko` subcommand:

- Signature
- Flags
- What it does
- Example

`reference/mcp.html` — every MCP tool:

- Name, params (JSON schema), return shape
- Tier requirement
- Example call (mcpc style)

`reference/schema.html` — every SQLite table:

- Columns with types and meaning
- Which daemon owns writes
- Key indexes

`reference/grants.html` — grant rule syntax, tier defaults, examples.
`reference/jid.html` — JID format, all typed forms, glob matching.

### Howto (`howto/`)

Audience: first-time operator. Getting started, step by step.
`howto/index.html` covers: system requirements, install, first instance,
first channel, first invite, verify agent responds.

Additional howto pages (future): upgrade, backup, multi-instance,
custom skills, custom products.

## Voice

**Warm caveman.** Picture a senior engineer at a whiteboard,
walking a colleague through how the thing actually works. Patient,
plain, concrete. No buzzwords, no theatre, but not robotic either —
a small aside or dry observation is welcome when it lands. Roughly
30% of the way from Mr Spock toward warm; if you ever feel yourself
reaching for the marketing register, you've gone too far.

Make explanations accessible without dumbing them down. The reader
is bright and curious; treat them that way.

**Use:**

- Concrete nouns and verbs. "Proxyd writes a signed header" beats
  "Proxyd performs identity attestation."
- File paths, daemon names, SQL column names where they sharpen
  meaning: `gated`, `proxyd`, `auth/middleware.go:RequireSigned`,
  `messages.db`, `acl` table.
- Direct address — "you" is fine. "Once you've set this, the
  agent stays in the thread."
- Contractions where natural. "It's", "doesn't", "won't".
- One picture per paragraph. Don't pile abstractions.
- Examples — show, don't just describe. A grants page without an
  example rule teaches nothing.
- Causation: "when X arrives, Y happens because Z."
- An occasional dry aside if it earns its place ("there's no
  managed control plane — that's the feature, not an oversight").

**Avoid:**

- Marketing adjectives ("powerful", "robust", "elegant", "seamless",
  "scalable", "intuitive"). Each one signals theatre over truth.
- Abstract nouns when a concrete one exists. "Abstraction" → name
  the thing (a table, a row, a file, a column). "Primitive" → name
  the thing. "Subsystem" → name the daemon. "Tenancy" → "groups"
  or "folders". "Surface" → "URL" or "endpoint" or "page".
- Three-noun stacks ("a typed, scoped, composable primitive").
- "Note that", "It's important to understand", "As you can see".
- Emoji. Exclamation marks.
- "We" / "us" — say what the system does, or address "you".
- Sentences over 30 words.
- Headers that aren't doing real work — if a section has two
  lines, fold it into the parent.
- Forced warmth: "great news!", "we're thrilled to", "love this
  for you". Warmth must be a side-effect of clear writing, never
  a layer on top.

**Trick to apply:** read each paragraph imagining you're explaining
it to someone you respect, in person, over coffee. If you'd stop to
unpack a word, replace it. If you'd skim past a sentence, sharpen
the image. If they'd ask "wait, how does that actually work" — you
owe a sentence. If your draft sounds like a press release, scrap
that paragraph.

## Style rules

> **Arizuko visual identity is load-bearing — keep it.** Borrow IA + content
> patterns from external references (Divio four-category, Stripe three-column,
> dbt's reference-page rhythm cited 2026-05-25) but **do NOT adopt their
> visuals**. The hub.css palette, 2px corners, dense typography, and arizuko
> color twists stay. The job of an external reference is to inform structure
> and tone; the look is ours.

All pages use `hub.css` + `hub.js`. Use **relative paths** for all
internal links and asset references — never `/pub/...` absolute paths.
The docs are served from a subpath (`/pub/arizuko/`) and absolute paths
break navigation. Use `../assets/hub.css`, `../../assets/hub.js`, etc.
based on the file's depth under `pub/`.

```html
<div class="hub-container prose">
  <p class="dim back"><a href="../">arizuko</a> › section › page</p>
  <h1>Page title</h1>
  …
</div>
<button class="theme-toggle">🌙</button>
<script src="../assets/hub.js"></script>
```

Breadcrumb: always present, links to parent sections. Format: `arizuko › components › gated`.

Code: use `<pre><code>` — hub.css styles it. Shell commands get no
prompt prefix; bash comments are fine.

Callout boxes:

```html
<div class="callout"><strong>Note:</strong> text here.</div>
```

No framework, no build tooling. Edit HTML directly. Test with any
static file server or by deploying to a running instance (`/pub/`).

## Adding a new product

1. Create `pub/products/<name>/index.html` (intro) and `setup.html`
2. Add a row to the product grid in `pub/products/index.html`
3. Add a link to the landing page `go deeper` section if it's a flagship product
4. Create `specs/6/product-<name>.md` (if not already there)

## Adding a new component page

1. Create `pub/components/<daemon>.html`
2. Add a row to `pub/components/index.html`
3. Link from `pub/reference/env.html` in the daemon's env var section

## Migration from concepts/

The `concepts/` pages predate this structure. They are:

- `slink.html`, `slink-reference.html` → migrate to `components/slink.html` + `reference/` (keep concepts/ as redirect)
- `ant.html` → migrate to `components/ant.html`
- `grants.html` → migrate to `reference/grants.html`
- `jid.html` → migrate to `reference/jid.html`
- `auth.html` → stays in concepts/ (cross-cutting, not a component)
- `routing.html` → stays in concepts/ (cross-cutting)
- `voice.html` → migrate to `components/ttsd.html` + mention in channels
- `webdav.html` → migrate to `components/davd.html`

Don't delete concepts/ pages until all external links are redirected.
`crackbox/` and `slink/` top-level dirs → redirect to `components/`.

## Current state (2026-05-04)

Exists: landing, concepts/ (ant, auth, grants, jid, routing, slink,
slink-reference, voice, webdav), crackbox/ (component + reference),
slink/ (component + reference), howto/, changelog/.

Missing: products/ (all), components/ (all, content exists in concepts/ and crackbox/),
reference/ (all, content exists in concepts/).
