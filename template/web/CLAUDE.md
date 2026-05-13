# arizuko doc site

Static HTML served at `/pub/` by vited. Source lives in
`template/web/pub/` in the arizuko repo; it is copied verbatim
into each instance's `<data-dir>/web/pub/` on `arizuko create`.
No build step — plain HTML + one CSS file + one JS file.

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
- Tuning (SOUL.md customisation, skill enable/disable)

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

## Style rules

All pages use `hub.css` + `hub.js`. Patterns:

```html
<div class="hub-container prose">
  <p class="dim back"><a href="/pub/">arizuko</a> › section › page</p>
  <h1>Page title</h1>
  …
</div>
<button class="theme-toggle">🌙</button>
<script src="/pub/assets/hub.js"></script>
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
