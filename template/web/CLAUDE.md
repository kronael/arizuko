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
    ant.html              agent-as-folder unit
    slink.html            public web endpoint
    proxyd.html           auth proxy
    webd.html             web channel + SSE hub
    crackbox.html         egress sandbox (old crackbox/ redirects here)
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
    grants.html           grant rule syntax
    jid.html              JID format reference
  howto/
    index.html            getting started — install, create instance, first message
    chat-sdk.html         embed chat with arizuko-client.js (old examples/ redirects here)
  concepts/               EXPLANATION — guided walkthrough, curriculum-ordered (spec 5/D); narrative twins of reference nouns
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
10% of the way from Mr Spock toward warm; if you ever feel yourself
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
> visuals**. The hub.css palette, its corner radii (6/4/3px today — hub.css
> is the source of truth, not any px figure), dense typography, and arizuko
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

**Callout style: a quiet hairline-bordered inset tint — NEVER a
solid-fill bar and NEVER a left-accent bar. Both read as LLM slop.**
The `.callout` in hub.css is: a 1px `color-mix(accent 22%)` border all
around, the house corner radius, a muted `color-mix(accent 7%, bg)`
tint, and a gold accent label (the leading `<strong>`, coloured
`var(--accent)`). The label is the only accent; the box stays quiet.
Do NOT reintroduce `background: var(--card)` (solid fill) or
`border-left: 3px solid var(--accent)` (the left bar) — those were the
slop that got removed. Dual-theme via `color-mix` tokens, no hardcoded
hex/px that fight the scale.

**Same no-slop rule for the nav current item.** The active nav link
(`.docs-nav a[aria-current="page"]`) is `color: var(--accent)` +
`font-weight: 600` ONLY — a quiet weight/colour shift. NEVER a
solid/tint background fill (`background: color-mix(... --accent ...)`)
and NEVER a `border-left` accent bar. Both read as the same LLM slop
that got pulled from the callouts; the highlight stays a typographic
cue, not a painted block.

No framework, no build tooling. Edit HTML directly. Test with any
static file server or by deploying to a running instance (`/pub/`).

## Maintenance — keep docs current (same-commit rule)

Docs are part of a change, not a later chore. **When you change a
surface, update its page in the same commit.** Spec: `specs/5/D`.

| You changed…                   | Update…                                                                                                                   |
| ------------------------------ | ------------------------------------------------------------------------------------------------------------------------- |
| a CLI command / flag           | `reference/cli.html`                                                                                                      |
| an env var                     | `reference/env.html`                                                                                                      |
| an MCP tool (name, args, tier) | `reference/mcp.html`                                                                                                      |
| a DB schema migration          | `reference/schema.html`                                                                                                   |
| a grant scope or the grant DSL | `reference/grants.html` + `concepts/grants.html` + `concepts/scopes.html`                                                 |
| JID / token / topic grammar    | the `reference/*` page + its `concepts/*` twin                                                                            |
| a new daemon                   | `components/<daemon>.html` + `components/index.html` + the `reference/openapi.html` row                                   |
| a new channel adapter          | `components/<adapter>.html` (+ a `howto/` recipe if setup is non-trivial)                                                 |
| a new concept / primitive      | a `concepts/<x>.html` page **and** slot it into the curriculum order in `concepts/index.html`, fixing neighbouring pagers |
| a tagged release               | `changelog/index.html`                                                                                                    |

Discipline:

- **Concepts is an ordered curriculum**, not an alphabetical set. A new
  concept goes where it belongs in the learning arc — re-stitch
  `concepts/index.html` order and the prev/next pagers of its
  neighbours. Don't default to appending at the end.
- A new `reference/` page updates the inlined left-nav tree in its
  sibling pages, same commit (static-nav drift).
- Verify-before-announce: every touched `/pub/*` URL returns 200 before
  you call it done, then sync to krons (see Deploy procedure above).

## Adding a new product

1. Create `pub/products/<name>/index.html` (intro) and `setup.html`
2. Add a row to the product grid in `pub/products/index.html`
3. Add a link to the landing page `go deeper` section if it's a flagship product
4. Create `specs/6/product-<name>.md` (if not already there)

## Adding a new component page

1. Create `pub/components/<daemon>.html`
2. Add a row to `pub/components/index.html`
3. Link from `pub/reference/env.html` in the daemon's env var section

## concepts/ ↔ reference split (spec 5/D)

`concepts/` is NOT dissolved — it is the Explanation **walkthrough**
(curriculum-ordered, spec 5/D). The ownership rule:

- A noun needing both narrative and exhaustive surface (grants, jid,
  tokens, topics) keeps BOTH: `concepts/<x>.html` (narrative, in the
  walkthrough) and `reference/<x>.html` (exhaustive), cross-linked.
- Daemon detail lives in `components/<daemon>.html`; the cross-cutting
  _idea_ it implements stays a concept (`concepts/voice.html` is the
  idea, `components/ttsd.html` is the daemon).

Real top-level moves (only these): `crackbox/` → `components/crackbox.html`;
`examples/chat-sdk.html` → `howto/`. Redirect the old paths.

## Current state (2026-05-29)

Populated: landing, `concepts/` (~20 pages), `components/` (~22 daemon
pages), `reference/` (11 pages), `products/`, `howto/`, `security/`,
`changelog/`, plus legacy `crackbox/` + `examples/` pending the moves
above. Pending (spec 5/D): the shared three-pane chrome (inline
`<style>` still per-page), the reference-rhythm reflow, and the
concepts curriculum order.
