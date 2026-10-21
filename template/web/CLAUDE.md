# arizuko doc site

Static HTML served at `/pub/` by vited. Source lives in
`template/web/pub/` in the arizuko repo; it is copied verbatim
into each instance's `<data-dir>/web/pub/` on `arizuko create`.
No build step — plain HTML + one CSS file + one JS file.

## Deploy to live krons (operational procedure)

This file is the ONLY deploy procedure — root CLAUDE.md points here and
carries no command of its own, because a second copy drifted into a
destructive one.

The source is `template/web/pub/arizuko/`, NOT `template/web/pub/`. The
webroot is `/pub/`; `/pub/arizuko/` is one site under it. Syncing the
parent copies `arizuko/` and `assets/` INTO `arizuko/` — that is where
the stray `/pub/arizuko/arizuko/` on live came from — and with a
`--delete` flag it removes every live content directory instead.

The local permission policy blocks `rsync`, and the live target
contains files that must not be deleted, so the workflow is:

```bash
# 1. Copy contents in (no sudo — target is owned by onvos)
cp -r template/web/pub/arizuko/. /srv/data/arizuko_krons/web/pub/arizuko/

# 2. Diff to find stale files left over (cp doesn't delete)
find template/web/pub/arizuko -type f -printf '%P\n' | sort > ./tmp/src.list
find /srv/data/arizuko_krons/web/pub/arizuko -type f -printf '%P\n' | sort > ./tmp/dst.list
comm -13 ./tmp/src.list ./tmp/dst.list        # live-only: the stale ones

# 3. Delete only verified-stale files (sudo — files owned by mivu/root).
#    PRESERVE: ./CLAUDE.md, ./.nomigrate, ./*.bak-krons, ./skills-export/**
#    Print the list FIRST and read every line. Never expand a glob or a
#    <placeholder> straight into a delete — a wrong path here wipes the site.
sudo find /srv/data/arizuko_krons/web/pub/arizuko -name '*.bak-*-style' -print
sudo find /srv/data/arizuko_krons/web/pub/arizuko -name '*.bak-*-style' -delete
# Stale subtrees (./cookbooks/, ./docs/, ./blog/, ./arizuko/): list them,
# confirm each against step 2's diff, then remove by explicit named path —
# one command per directory, never a wildcard.

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

Each directory's `index.html` lists its own pages, so this map names the
sections and what each one answers. Do not hand-list every file here —
a second copy of the tree drifts the moment a page is added.

```
pub/arizuko/
  index.html      landing — what arizuko is, quick start
  products/       one directory per product: index.html (the pitch) + setup.html
  components/     one page per daemon, plus channels.html for the adapter set
  concepts/       EXPLANATION — guided walkthrough, curriculum-ordered;
                  the narrative twin of a reference noun
  reference/      LOOKUP — env vars, CLI, MCP tools, store tables, grants,
                  JIDs, OpenAPI, packages, route tokens, topics
  howto/          guided procedures, product-branded
  security/       threat model and the operator's side of it
  strategy/       positioning and direction; outside the sidebar, linked
                  from the landing
  research/       a dated write-up; outside the sidebar, linked from the
                  landing
  changelog/      what shipped, dated
  assets/
    hub.css       single stylesheet (dark/light, prose, code, nav)
    hub.js        theme toggle, minimal nav, the version stamp
```

Two rules the tree enforces:

- The sidebar has one declaration, `template/web/nav.json`: the sections in
  order and, for each section the sidebar expands, its pages in order. Every
  page holds a static copy of the sidebar, so the site needs no script to show
  it. After an edit to `nav.json`, run `make docs-nav`: it writes the render
  into every page. `tests/webdocs` `TestWebDocsNavIsGenerated` fails on a
  page whose copy differs, and `TestNavDeclaresEveryPage` fails on a page in a
  listed section that `nav.json` does not list.
- A new daemon needs THREE edits: its own `components/<name>.html`, a row in
  `components/index.html`, and its line in `nav.json`.
- `hub.js` holds `ARIZUKO_VERSION`, the only copy. Bump it when you tag.

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

Each component page answers four questions in order. Each `h2` states the
answer as a claim, never the question itself, for example "egress is a forward
proxy for one client and one fixed list":

1. **What is it?** One sentence. Not marketing — mechanism.
2. **Why does it exist?** The system-design reason. What would break without it.
3. **How does it fit?** Callout box: inputs → daemon → outputs. Deps.
4. **Standalone usage.** How to run it outside arizuko (config, ports, auth).
   If it can't run standalone, say so and explain why.

`components/index.html` — table of all daemons: name | kind (core/integration)
| one-line role | link. Same data as README.md daemon table — keep in sync.

Component pages run ~120–150 lines of HTML; the shared chrome carries the
nav, so the body is the four answers and little else. Do not pad a page to
reach a length. Link to reference/env.html anchored to that daemon's section.

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
- Required grant (`mcp:<tool>`)
- Example call (mcpc style)

`reference/schema.html` — every table the store serves:

- Columns with types and meaning
- Which daemon owns writes
- Key indexes

`reference/grants.html` — the acl row, action lattice, roles, delegation, examples.
`reference/jid.html` — JID format, all typed forms, glob matching.

### Howto (`howto/`)

Audience: first-time operator. Getting started, step by step.
`howto/index.html` covers: system requirements, install, first instance,
first channel, first invite, verify agent responds.

Additional howto pages (future): upgrade, backup, multi-instance,
custom skills, custom products.

## Voice

Every page uses the plain technical register of the `writing` skill
§ Language: ASD-STE100 Simplified Technical English, the same register as
every README. Read that section; the rules here only add what is specific to
this site.

- One word, one meaning. Active voice with a named actor, often a daemon:
  "`proxyd` writes a signed header", never "a signed header is written".
- One main clause per sentence, at most 25 words. No contractions, no
  metaphor, no idiom, no dry asides, no parentheses in prose.
- File paths, daemon names and column names where they make the meaning exact:
  `proxyd`, `auth/middleware.go`, the `acl` table.
- An example per concept. A grants page without an example row teaches
  nothing.
- "You" is fine. NEVER "we" or "us": say what the system does.
- NEVER marketing adjectives, "Note that", emoji or exclamation marks.
- A page title, the `h1` and a sidebar label are short nouns. Every section
  heading, callout label and figure title inside a page is a claim, or the
  reader's question when its first sentence answers it.
- A link to code or a repo file is a citation: short visible text in square
  brackets, `[auth/README.md]`. A page NEVER links a spec; state the fact.
- Abstract nouns only where no concrete one exists: name the table, daemon,
  folder, URL or endpoint.

**Honesty guardrails.** These claims are false, and a page that makes
one has to be fixed, however good it sounds:

- "Orthogonal" in the strict sense. The primitives each do one job in a
  fixed pipeline with layered overrides and no special cases; say that,
  and name the seams where they touch.
- "Tiers" or "levels" of authority. Authority is grants; a folder's depth
  means nothing.
- "One database is the only truth." State is two durable stores: the
  store and the group folder on disk. The folder is not a cache.
- "Workflow engine." A workflow is a discipline the agent follows, not a
  primitive arizuko runs.

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

Every page copies the shell in §"Visual design system" below. Only the landing
page is shell-less, in a bare `.hub-container`; `tests/webdocs`
`TestWebDocsOnlyTheLandingIsShellLess` fails on any other page without the
shell.

Breadcrumb: always present, links to parent sections. Format: `arizuko › components › gated`.

Code: use `<pre><code>` — hub.css styles it. Shell commands get no
prompt prefix; bash comments are fine.

Callout boxes:

```html
<div class="callout">
  <strong>Engagement overrides the route table.</strong> text here.
</div>
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

### Reference-page rhythm

A `reference/` page follows one rhythm: H1, a one-sentence lede, the
definition, usage, examples, troubleshooting, then the pager and the footer.
A catalogue page (`cli`, `env`, `mcp`, `schema`, `openapi`) groups its items
as H3 under H2s by concern. A leaf page (`grants`, `jid`, `tokens`, `topics`)
follows the rhythm literally.

- One captioned code block per shape. Never a tab widget.
- Type, default and "required" go into the definition prose. A table earns its
  place only when it compares items.
- 3 to 6 H2 on a page, H3 only where an item is named, no H4.
- A version difference is an inline sentence ("Available since v0.45.4"),
  never a version banner.

Do not adopt these from other doc sites: Docusaurus, React or the dbt look; a
version selector, because the site has one current version and the footer
stamp tells the reader how old a page is; a tabbed code widget; reading-time
or feedback widgets; a hosted search index.

### Two footers and no search index

`injectFooter()` in `hub.js` writes the site-wide footer with the version
stamp. The page writes its own `.docs-footer`: the edit link and
`<time class="docs-updated">`, as in §"The shell a page copies".

The `stamp-docs` hook in `.pre-commit-config.yaml` runs `stamp-docs.py` on
every staged page. The script writes the date of the file's last commit into
that `<time>`. The hook runs before the new commit exists, so the date is the
one of the page's previous commit. When the hook rewrites a page, the commit
fails: stage the page again and commit again. Without git metadata the script
keeps the existing stamp and never blanks it.

The reader finds things through the arizuko agent on krons, not through a
search index. `injectAskAgent()` and `injectSelectionPopup()` open a chat with
that agent, and the top-bar search box that `fillTopbar()` adds sends its
query to the same chat. A page never adds a search widget of its own.

## Maintenance — keep docs current (same-commit rule)

Docs are part of a change, not a later chore. **When you change a
surface, update its page in the same commit.**

| You changed…                    | Update…                                                                                                                   |
| ------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| a CLI command / flag            | `reference/cli.html`                                                                                                      |
| an env var                      | `reference/env.html`                                                                                                      |
| an MCP tool (name, args, grant) | `reference/mcp.html`                                                                                                      |
| a DB schema migration           | `reference/schema.html`                                                                                                   |
| a grant scope or the grant DSL  | `reference/grants.html` + `concepts/grants.html` + `concepts/scopes.html`                                                 |
| JID / token / topic grammar     | the `reference/*` page + its `concepts/*` twin                                                                            |
| a new daemon                    | `components/<daemon>.html` + `components/index.html` + the `reference/openapi.html` row                                   |
| a new channel adapter           | `components/<adapter>.html` (+ a `howto/` recipe if setup is non-trivial)                                                 |
| a new concept / primitive       | a `concepts/<x>.html` page **and** slot it into the curriculum order in `concepts/index.html`, fixing neighbouring pagers |
| a tagged release                | `changelog/index.html`                                                                                                    |

Discipline:

- **Concepts is an ordered curriculum**, not an alphabetical set. A new
  concept goes where it belongs in the learning arc — re-stitch
  `concepts/index.html` order, its line in `nav.json` and the prev/next
  pagers of its neighbours. Don't default to appending at the end.
- Verify-before-announce: every touched `/pub/*` URL returns 200 before
  you call it done, then sync to krons (see Deploy procedure above).

## Adding a new product

1. Create `pub/products/<name>/index.html` (intro) and `setup.html`
2. Add a row to the product grid in `pub/products/index.html` and the entry
   in `nav.json`, then run `make docs-nav`
3. Add a link to the landing page `go deeper` section if it's a flagship product
4. Create `specs/7/product-<name>.md` (if not already there)

A product with no tree in `ant/examples/` is a shape. A shape gets a section
in `pub/products/shapes.html`, never a page pair, and the grid row links that
section.

`tests/webdocs` `TestProductCatalogMatchesCorpus` gates steps 1–2: every tree
in `ant/examples/` must have both pages, every product directory must have a
tree, and `shapes.html` must carry the "This is a shape, not a template" note
and no section for a shipped product. The ritual existed and was skipped
anyway — six shipped products had no page and four pages had no tree (BUGS
`L38`), which is why it is a test now and not only a list.

## Adding a new component page

1. Create `pub/components/<daemon>.html`
2. Add a row to `pub/components/index.html`
3. Link from `pub/reference/env.html` in the daemon's env var section

## concepts/ ↔ reference split

`concepts/` is NOT dissolved — it is the Explanation **walkthrough**
(curriculum-ordered, §"Maintenance" Discipline). The ownership rule:

- A noun needing both narrative and exhaustive surface (grants, jid,
  tokens, topics) keeps BOTH: `concepts/<x>.html` (narrative, in the
  walkthrough) and `reference/<x>.html` (exhaustive), cross-linked.
- Daemon detail lives in `components/<daemon>.html`; the cross-cutting
  _idea_ it implements stays a concept (`concepts/voice.html` is the
  idea, `components/ttsd.html` is the daemon).

## Current state

The file count moves, so read the tree rather than a number here: `find template/web/pub/arizuko -type f | wc -l` against
`find <data-dir>/web/pub/arizuko -type f | wc -l` tells you whether live and
source agree, and `comm -13` on the two sorted lists names anything live is
carrying that source does not.

## Visual design system

`assets/hub.css` is the one stylesheet and the one place a value is written.
A page carries classes and nothing else. The reference page every other page
copies is `concepts/primitives.html`.

### Tokens

Every token is a custom property on `:root` in `assets/hub.css`. Light is the
base. Dark is redefined twice: under `@media (prefers-color-scheme: dark)` for
the paint before `hub.js` runs, and under `:root[data-theme="dark"]` for the
toggle. Edit both when you change a dark value.

Colour — these are the palette and they do not change:

| Token       | Holds                                                     |
| ----------- | --------------------------------------------------------- |
| `--bg`      | the page background                                       |
| `--card`    | a raised box: card, tablecard, top-bar control            |
| `--code-bg` | code span, `pre`, hover wash                              |
| `--border`  | every hairline                                            |
| `--bright`  | a table head, the one word on a page that must win        |
| `--fg`      | body text                                                 |
| `--dim`     | metadata, captions, an inactive nav link                  |
| `--accent`  | gold: `h1`, links, the active nav link, the callout label |
| `--accent2` | blue: secondary emphasis inside prose                     |
| `--accent3` | pale blue: `h2`, card titles, section headers             |

Type — the body face is monospace and stays monospace:

| Token                    | Holds                                          |
| ------------------------ | ---------------------------------------------- |
| `--font-mono`            | the one family                                 |
| `--fs-root`              | body size; every size below is relative to it  |
| `--fs-xs`                | TOC label, version stamp                       |
| `--fs-sm`                | caption, meta, note, footer, pager             |
| `--fs-md`                | table, nav, secondary prose                    |
| `--fs-base`              | body copy                                      |
| `--fs-lg`                | lede, `h3`                                     |
| `--fs-xl`                | `h2`                                           |
| `--fs-2xl`               | `h1`                                           |
| `--lh-tight`/`--lh-base` | heading and code line height; body line height |

Spacing is one scale, `--sp-0` (0.15em) through `--sp-7` (3em). A gap that is
not on the scale is a mistake.

Radius is three steps: `--r-sm` for a code span and a small control,
`--r-md` for a callout, a `pre` and a tablecard, `--r-lg` for a card.

Shadow is `--shadow-1` and `--shadow-2`. Only a floating element casts one.

Layout: `--measure` is the prose line length, `--w-content` the content
column, `--w-nav` and `--w-toc` the two rails, `--w-page`/`--w-guide`/
`--w-prose` the three container widths, `--topbar-h` the top bar, `--tap` the
minimum touch target.

### The shell a page copies

```html
<body>
  <header class="docs-topbar">
    <button
      class="docs-navtoggle"
      aria-label="Toggle navigation"
      aria-expanded="false"
    >
      &#9776;
    </button>
    <a class="docs-wordmark" href="../">arizuko</a>
  </header>
  <div class="docs-layout">
    <nav class="docs-nav" aria-label="Concepts">
      <ul>
        <li>
          <a class="nav-section" href="./">Concepts</a>
          <ul class="nav-children">
            <li><a href="primitives.html">Primitives</a></li>
          </ul>
        </li>
        <li><a class="nav-section" href="../reference/">Reference</a></li>
      </ul>
    </nav>
    <main class="docs-content">
      <p class="docs-crumb">
        <a href="../">arizuko</a> &rsaquo; concepts &rsaquo; primitives
      </p>
      <h1>primitives</h1>
      <p class="lede">One sentence that says what the page answers.</p>
      <h2>A section</h2>
      <nav class="docs-pager" aria-label="Pager">
        <a rel="prev" href="./">&larr; Concepts</a>
        <a rel="next" href="built.html">How it&rsquo;s built &rarr;</a>
      </nav>
      <div class="docs-footer">
        <a
          href="https://github.com/kronael/arizuko/edit/main/template/web/pub/arizuko/concepts/primitives.html"
          >Edit this page</a
        >
        &middot; updated
        <time class="docs-updated" datetime="2026-10-16">2026-10-16</time>
      </div>
    </main>
    <aside class="docs-toc" aria-label="On this page"></aside>
  </div>
  <button class="theme-toggle">&#127769;</button>
  <script src="../assets/hub.js"></script>
</body>
```

`hub.js` fills four things, so a page never writes them: the right rail from
the page's own `h2` and `h3`, `aria-current="page"` on the matching nav link,
the version and the search slot in the top bar, and the move of
`.theme-toggle` into the bar. Leave `.docs-toc` empty and leave
`aria-current` out of the markup — a hand-written copy of either drifts.

The sidebar lists the seven sections in `nav.json` and expands the one the
reader is in. Strategy and research are outside it; the landing links them. The top bar is sticky at every width. Below 1200px the sidebar collapses
behind `.docs-navtoggle` and the right rail drops.

### Components

| Component  | Markup                                                                          |
| ---------- | ------------------------------------------------------------------------------- |
| callout    | `<div class="callout"><strong>Claim.</strong> text.</div>`                      |
| code block | `<pre><code>…</code></pre>` — the box scrolls, the page does not                |
| table      | a plain `<table>`; `hub.js` wraps it in `.table-scroll`; hairline rows, no fill |
| card       | `<div class="card"><div class="card-title">…</div>…</div>`, in a `.grid`        |
| pager      | `<nav class="docs-pager"><a rel="prev" …><a rel="next" …></nav>`                |

The callout's quiet-inset rule and the nav current-item rule are in
§"Style rules" above. Read them before you touch either.

### Forbidden

- A per-page `<style>` block. The value belongs in `hub.css` under a class
  name, or the page does not need it.
- An inline `style=` carrying a colour, a token included. Move the rule into
  `assets/hub.css` under a class.
- A Title Case heading. Headings are sentence case; section headings are
  claims (see § Voice).
- A gradient box.
- A left-accent bar on any box.
- A raw hex, rgb or px colour anywhere outside the `:root` token blocks.

`tests/webdocs` `TestWebDocsPagesCarryNoStyleOfTheirOwn` fails the build on a
`<style>` tag and on a `style=` attribute carrying a colour. Its exception list
is empty.
