---
status: draft
depends: []
---

# specs/5/D — docs refs redesign (dbt IA + content patterns over arizuko visuals)

## Why

`/pub/arizuko/reference/` works as a flat catalogue (9 pages, ~4200
lines HTML), but each page invented its own chrome: per-page inline
`<style>` blocks, ad-hoc breadcrumb formats, mixed TOCs, no consistent
closer (no edit-this-page, no prev/next, no last-updated). The reader
can't tell where they are in the reference set.

dbt docs solves this at scale: three-pane chrome, breadcrumb + H1 +
definitional first sentence, low heading density, captioned single-
language code blocks, inline cross-refs, prev/next pager. IA + content
discipline transfer cleanly; visuals do not.

> Arizuko visual identity is load-bearing — keep it. Borrow IA + content
> patterns from external references (dbt's top-level taxonomy, Stripe
> three-column, dbt's reference-page rhythm cited 2026-05-25) but do
> NOT adopt their visuals. The hub.css palette, 2px corners, dense
> typography, and arizuko color twists stay. The job of an external
> reference is to inform structure and tone; the look is ours.

Phase 1 (reference/ — 9 pages) in detail; Phase 2 (concepts/, howto/,
examples/, products/) sketched below.

## Sources

- Research: `/srv/data/arizuko_krons/groups/krons/facts/dbt-docs-design-study.md`
  (21KB, 7 dbt URLs, IA observations + content patterns + per-page-type
  templates + voice/tone + positioning). Authoritative reference for
  every dbt observation cited below.
- Companion: `/srv/data/arizuko_krons/groups/krons/facts/technical-guide-structure-patterns.md`
  — cross-vendor structural patterns informing the five-section IA.
- Current refs: `template/web/pub/arizuko/reference/*.html` (9 pages:
  `cli`, `env`, `grants`, `index`, `jid`, `mcp`, `schema`, `stats`,
  `tokens`, `topics`)
- Style guide: `template/web/CLAUDE.md` — "Voice", "Style rules"
- Visual-identity guard: root `CLAUDE.md` "Updating the web docs"
  (commit `4c93c49`)

## Information architecture

Five top-level sections under `/pub/arizuko/`, taken from the dbt-docs
design study. The sections are NOT parallel — each answers a different
reader posture.

| Section      | What it is                                                                                       | Arizuko example                                                                                       |
| ------------ | ------------------------------------------------------------------------------------------------ | ----------------------------------------------------------------------------------------------------- |
| `concepts/`  | Narrative explanation of arizuko's mental model — what the system is, how the primitives relate. | "Routing", "Groups and folders", "Grants and tiers"                                                   |
| `howto/`     | Task-oriented guides; reader knows what they want and needs the recipe.                          | "Add a Slack adapter to a running instance", "Run crackbox standalone", "Write a migration"           |
| `reference/` | Exhaustive API/CLI/MCP/config surface — every daemon, every env var, every tool.                 | `cli.html`, `env.html`, `mcp.html`, `schema.html`                                                     |
| `examples/`  | Working snippet/recipe demonstrating a pattern; reader is exploring, no fixed task.              | A `/v1/audio/speech` curl producing voice via ttsd; a YAML manifest adding a webhook + cron + ACL row |
| `products/`  | Shippable, brandable pieces built on arizuko primitives.                                         | hub.css starter kit; channel-adapter packs; the public docs site itself                               |

Howto = "do this". Examples = "look at this working". Products = "ship
this". Concepts is narrative; reference is exhaustive. The five
sections are not parallel and do not get forced into a uniform shape.

## What we adopt from dbt

1. **Three-pane layout at wide viewport.** Left: section tree
   (Concepts / How-To / Reference / Examples / Products, current
   section expanded). Middle: content, capped width. Right: page-
   internal TOC built from H2/H3. Breakpoint ≥1200px three-pane;
   <1200px single column, sidebar in a drawer, right TOC absent.
2. **Breadcrumb above H1 on every page**, two-to-three segments, each
   ancestor linked. Format: `arizuko › reference › CLI commands`.
3. **Reference-page content template**: breadcrumb → H1 (bare name for
   leaves, "About <thing>" for catalogue overviews) → definitional first
   sentence → H2 "Definition" (type/default/required folded into prose)
   → H2 "Usage" or "Recommendation" → H2 "Examples" with captioned code
   blocks → optional H2 "Troubleshooting" with H3 named failures.
4. **One captioned code block per shape.** Caption is filename or language
   (`bash`, `yaml`, `sql`). Multi-shape commands ship as sequential blocks,
   not a tab widget.
5. **Type/Default/Required folded into Definition prose**, not a fielded
   table at the top. Tables earn their place for cross-item comparison.
6. **Previous/Next pager at page foot**, keyed off left-nav order.
7. **Edit-this-page + last-updated stamp** at page foot. Links to
   `github.com/kronael/arizuko/edit/main/template/web/pub/arizuko/<path>`.
8. **Inline version-difference statements, not banners.** "Available
   since v0.45.4" stays in prose where it matters.
9. **Callouts rare and reserved for footguns.** Precedence rules,
   judgment, normal-but-important behaviour stay in prose.
10. **Voice discipline.** Short sentences, present tense, concrete nouns,
    recommendations stated as recommendations, failure modes named by
    observable symptom. Aligns with `template/web/CLAUDE.md` "warm
    caveman" voice — dbt's register is already close.

## What we do NOT adopt

- **Docusaurus chrome, fonts, colours, rounded corners** — hub.css
  palette, 2px corners, dense typography stay.
- **Version-selector dropdown in top bar** — one current site; dropdown
  reading "v(current)" is noise. Open question below.
- **Tabbed code-block widget** — adds JS and divergence surface;
  sequential captioned blocks stay static-HTML.
- **Estimated reading time, time-to-read, last-viewed-by** — noise.
- **Marketing-footer block** — our footer stays minimal: repo link,
  changelog link, theme toggle, version stamp.
- **Hero/marketing voice under `/reference/`** — reference is for the
  reader who has decided to use the thing.

## Discovery is conversational, not lexical

No in-page search. No hosted search index (Algolia, Meilisearch). The
agent is the search, via two mechanisms already shipped in
`template/web/pub/assets/hub.js`:

- **`injectAskAgent()`** — fixed bottom-right button that opens
  `https://krons.fiu.wtf/chat/<AGENT_TOKEN>/?ref=<page-path>`. The
  visitor lands inside a live arizuko chat with the page URL as
  context.
- **`injectSelectionPopup()`** — selecting 3–500 characters of body
  text surfaces an "Ask about this" popup; the selection plus page URL
  are handed to the same agent.

The `AGENT_TOKEN` is a public route token bound to the krons agent,
which has the codebase + docs in context. Asking the agent IS asking
the docs. Rate limiting lives at the webd layer (`chat_mcp.go`); the
token being public is a deliberate design choice.

## First-experience onboarding via the agent

A first-time visitor doesn't need a guided tour. The Ask-the-agent
button is the onboarding: a question routed to the krons agent
short-circuits the read-concepts-then-howto-then-reference linear
path. The select-to-ask popup is the fine-grained variant — highlight
a paragraph that doesn't parse, click "Ask about this", get an
in-context answer with the page URL attached as `ref=`. Both surfaces
exist on every page that loads `hub.js`; no per-page wiring.

## Version visibility

`hub.js:injectFooter()` writes `arizuko vX.Y.Z` into the page footer
on every load. The version string lives in the `ARIZUKO_VERSION`
constant at the top of `hub.js` (currently `v0.45.10`). It is bumped
by hand on each release — same commit that moves `CHANGELOG.md`
`[Unreleased]` to a dated heading. Until automated, the discipline is:
edit `template/web/pub/assets/hub.js`, then rsync per the root
`CLAUDE.md` "Updating the web docs" workflow.

## Three-pane layout

Implementation lives in `hub.css` + `hub.js`. No new framework, no build
step, plain HTML. The current per-page inline `<style>` blocks get
deleted — every page-level chrome rule moves into `hub.css`.

```
┌──────────────────────────────────────────────────────────────────────┐
│ top bar — arizuko wordmark · theme toggle                            │
├──────────┬───────────────────────────────────────┬───────────────────┤
│ nav      │ breadcrumb                            │ on this page      │
│          │ H1                                    │  • Definition     │
│ Concepts │ definitional first sentence           │  • Usage          │
│ How-To   │                                       │  • Examples       │
│ Reference│ H2 Definition                         │  • Troubleshoot   │
│  cli     │   prose, code blocks, inline links    │                   │
│  env     │ H2 Usage                              │                   │
│  …       │   …                                   │                   │
│ Examples │                                       │                   │
│ Products │ [← prev] · [next →]                   │                   │
│          │ Edit this page · last updated         │                   │
└──────────┴───────────────────────────────────────┴───────────────────┘
```

CSS structure (new classes added to hub.css):

- `.docs-layout` — CSS grid, three columns at ≥1200px, single column
  below
- `.docs-nav` — left tree; sticky position; nav data inlined per page
  (no JS fetch)
- `.docs-content` — middle column; inherits existing `.prose`
- `.docs-toc` — right column; sticky; built from H2/H3 by `hub.js`
- `.docs-pager` — prev/next at content foot
- `.docs-footer` — edit-this-page + last-updated stamp

`hub.js` gains one function: `buildTOC()` walks `.docs-content h2, h3`,
appends `<a>` entries to `.docs-toc ul`. Runs on DOMContentLoaded. ~20
lines.

## Reference-page content template

Skeleton each Phase-1 page is rewritten to match:

```html
<div class="docs-layout">
  <nav class="docs-nav"><!-- shared tree --></nav>
  <article class="docs-content prose">
    <p class="dim back">
      <a href="../">arizuko</a> › <a href="../reference/">reference</a> › CLI
      commands
    </p>
    <h1>CLI commands</h1>
    <p class="lede">
      Every subcommand of the <code>arizuko</code> binary, grouped by concern.
    </p>

    <h2>Definition</h2>
    <p>...type/default/required folded into prose...</p>
    <h2>Usage</h2>
    <p>...recommended pattern...</p>
    <h2>Examples</h2>
    <p class="caption">bash</p>
    <pre><code>arizuko create solo</code></pre>
    <h2>Troubleshooting</h2>
    <h3>Invalid project name</h3>
    <p>Symptom: <code>...</code>. Fix: ...</p>

    <nav class="docs-pager">
      <a rel="prev" href="env.html">← Environment variables</a>
      <a rel="next" href="mcp.html">MCP tools →</a>
    </nav>
    <footer class="docs-footer">
      <a href=".../edit/main/.../reference/cli.html">Edit this page</a>
      · Last updated 2026-05-26
    </footer>
  </article>
  <aside class="docs-toc">
    <p class="dim">On this page</p>
    <ul>
      <!-- built by hub.js -->
    </ul>
  </aside>
</div>
```

_Catalogue overviews_ (cli, env, mcp, schema — each documents many
items) keep per-item H3 structure but pin the top-level H2s to the dbt
rhythm. _Single-leaf references_ (grants, jid, tokens, topics) follow
the leaf template literally: Definition → Usage → Examples →
Troubleshooting.

## Nav model (left-rail tree)

Inlined per-page (no JS fetch) so the tree renders before script runs.
Order matches the section table on the docs landing and the prev/next
pager. Phase 2 sections appear as top-level entries from day one but
each holds only its existing `index.html` until Phase 2 ships content.

```
Concepts                   (placeholder — links to concepts/index.html)
How-To                     (placeholder — links to howto/index.html)
Reference
  Overview                  (index.html)
  CLI commands              (cli.html)
  Environment variables     (env.html)
  MCP tools                 (mcp.html)
  SQLite schema             (schema.html)
  Grants                    (grants.html)
  JID format                (jid.html)
  Topics                    (topics.html)
  Route tokens              (tokens.html)
  Codebase stats            (stats.html)
Examples                   (placeholder — links to examples/index.html)
Products                   (placeholder — links to products/index.html)
```

The tree is identical HTML on every reference page (copy-pasted; not
rendered from a JSON manifest — keeps the no-build-step promise). When
the page changes which section is "current", hub.js adds `aria-current=
"page"` to the matching `<a>` on DOMContentLoaded by comparing
`location.pathname`. ~5 lines of JS.

When adding a new reference page, update the tree in all sibling files
in the same commit. Drift is the price of statically inlined nav; it's
small enough at 9–15 pages that build tooling isn't worth it.

## Two-phase rollout (chrome vs content)

Split into two passes, each independently shippable:

1. **Chrome migration.** Add `.docs-layout`, `.docs-nav`, `.docs-toc`,
   `.docs-pager`, `.docs-footer` to hub.css. Add `buildTOC()` and
   `markCurrentNav()` to hub.js. Wrap each page's body in the new
   layout. Delete inline `<style>`. Normalise breadcrumb. Add prev/
   next + edit-this-page + last-updated. No content rewriting.
   Acceptance: every page renders three-pane on wide / single-column
   below; no palette / corner / typography drift.
2. **Content rewrite.** Reflow each page to the Definition / Usage /
   Examples / Troubleshooting H2 rhythm. Fold Type/Default/Required
   into Definition prose. Move source citations inline. Cull headings
   to dbt density (H2 count 3–6, H3 only where named, no H4).
   Acceptance: per-page review against the template skeleton.

Chrome first surfaces TOC + pager across the catalogue immediately;
content rewriting is then per-page work parallelisable across sessions.

## Phase 1 work list (9 reference pages)

Per page, in left-nav order. Each line: which template applies + the
shape of the rewrite. **Content-heavy** = rewriting prose + reshaping
H2s, not just chrome migration. **Chrome-only** = body content
substantially unchanged.

- **`index.html`** (90 lines today) — chrome-only. Section landing. Cards stay
  (link grid is the right shape for a section index), but inline
  styles move to hub.css; breadcrumb format normalised; gain prev/
  next pager pointing at first/last leaf; gain edit-this-page footer.

- **`cli.html`** (369 lines, 14 commands) — content-heavy. Catalogue-overview template.
  Top H1 "CLI commands" + lede stays. The current in-page `<div class="toc">`
  becomes the right-rail TOC (delete from middle column). H2 groups
  (lifecycle, chat, group, identity, …) stay as catalogue H2s; each
  subcommand H3 keeps its `Flag/Type/Default/Effect` table — those
  are cross-item comparisons within a single command's flag set, which
  is the one place tables earn their place. Strip inline `<style>`.

- **`env.html`** (1602 lines, ~120 vars across 24 sections) — content-
  heavy (largest reflow surface). Catalogue-overview. Same shape as
  cli — section H2s, per-var H3s. Each var's Type/Default/Effect/Example
  stays prose-folded into the Definition sentence where possible
  (matches dbt `name`); the existing tabular Default/Effect column
  where the value is genuinely a small enum earns the table treatment.
  Per-var inline source link kept (it's the load-bearing grep-
  verifiability claim from the index lede). Budget two passes: one to
  normalise the section H2 grammar, one to fold Default/Effect prose.

- **`mcp.html`** (730 lines, 45 tools) — content-heavy. Catalogue-overview. Tools
  grouped by concern (messages, social actions, history, …) as H2s,
  each tool H3 with: name, params JSON schema, return shape, tier
  badge, registration site link, example call. The tier badge stays —
  that's a real cross-cutting attribute; not a table per tool but a
  styled inline tag.

- **`schema.html`** (572 lines, ~25 tables) — content-heavy. Catalogue-overview.
  Per-table H3 keeps its column table (cross-column comparison earns
  it); writer/reader notes fold into prose. Migration history per
  table moves from inline list to a single "History" H4 under each
  table, matching dbt's named-subsection discipline.

- **`grants.html`** (195 lines) — chrome-only + light content edits. Leaf template. H1 "Grants" → lede
  defining grant DSL → H2 "Grammar" (rule syntax folded into prose
  with one EBNF-ish block) → H2 "Tier defaults" → H2 "Examples"
  (worked example from `GRANTS.md`) → H2 "Troubleshooting" (named
  failures: "no rule matches", "tier doesn't allow", etc).

- **`jid.html`** (216 lines) — chrome-only + unnumbering H2s. Leaf template. Current structure is
  already close — H2 numbering ("1. Wire form", "2. Code types", …)
  gets unnumbered to match dbt rhythm. Per-platform schema table
  stays (cross-platform comparison — earns its table). Gain
  Troubleshooting H2 with "JID parse error" + "kind missing" named
  cases.

- **`topics.html`** (161 lines) — chrome-only + light content edits. Leaf template. H2 "Definition" with
  topic schema → H2 "Creation paths" → H2 "Resolution order" → H2
  "MCP tools" → H2 "Examples". Narrative intro stays linked from the
  lede (it already lives at `concepts/topics.html`).

- **`tokens.html`** (167 lines) — chrome-only + light content edits. Leaf template. H2 "Definition" (the
  `route_tokens` table) → H2 "Mint surface" (MCP + REST) → H2 "URL
  prefixes" (`/chat/<token>/` + `/hook/<token>`) → H2 "Payload shapes"
  → H2 "SSE events" → H2 "Rate limits".

- **`stats.html`** (90 lines) — chrome-only. Leaf template, but degenerate. It's
  a one-page data dump (totals + per-package table). H1 "Codebase
  stats" + lede + the two tables stays; gain breadcrumb in canonical
  format, prev/next, edit-this-page. Otherwise unchanged.

Per-page implementation order: index first (touches every other
page's prev/next), then leaves (grants, jid, topics, tokens, stats),
then large catalogues (cli, env, mcp, schema). This lets us validate
the template on small pages before reflowing 1600-line ones.

## Phase 2 sketch

`concepts/`, `howto/`, `examples/`, `products/` inherit the same
three-pane layout and the same chrome (breadcrumb, prev/next, edit,
last-updated). Page-type templates from the research:

- **`concepts/`** — concept/introduction template from research §
  "Concept / introduction page". H1 question or noun, opening
  paragraph defining the thing in one outcome-shaped sentence, body
  H2s naming sub-engines or sub-mechanisms, closing "Related docs"
  H2 with bulleted links. Voice stays declarative, explanatory.
- **`howto/`** — how-to template from research § "How-To page". H1
  imperative ("Install arizuko", "Add a channel"), opening paragraph
  stating outcome, body H2 decision sections + imperative steps,
  closing prev/next pager (no checklist, no "you should now see ...").
- **`examples/`** — tutorial template. Not directly sampled in dbt's
  `/docs/...` namespace (dbt routes tutorials to `/guides/`). Phase-2
  redesign should sample one dbt guide before locking the template.
- **`products/`** — product page template. H1 product name, lede
  one-sentence pitch, H2 "What it is", H2 "How to consume" (npm tag,
  docker image, rsync snippet — whichever applies), H2 "Source", H2
  "Status". Each Products page is a shippable artifact; the page is
  its README on the web.

Each Phase 2 page-type inherits Phase 1's three-pane chrome and the
nav tree; the body H2 rhythm is what differs.

### Per-component howto pages for standalonable siblings

arizuko's design is that several sibling components are shippable
outside arizuko. The orthogonality grep at
[`specs/11/A-orthogonal-components.md`](../11/A-orthogonal-components.md)
enforces this for `auth|audit|resreg|obs` (extended 2026-05-26 at
commit `37c99a6`). Each shippable sibling earns a Phase 2 howto page:

- `howto/standalone-crackbox.html` — crackbox as a KVM-isolated
  sandbox outside arizuko (own `CLAUDE.md`, lives at
  `/home/onvos/app/crackbox/`).
- `howto/standalone-auth.html` — `auth/` as a capability library
  imported by any Go daemon (status: aspirational, see
  [`5/1-auth-standalone.md`](1-auth-standalone.md) Status blockquote).
- `howto/standalone-audit.html` — `audit/` as a thin audit-log writer.
- `howto/standalone-resreg.html` — `resreg/` as a resource registry.
- `howto/standalone-obs.html` — `obs/` as the OTLP wiring library
  (already zero arizuko-internal imports — shipped surface).
- `howto/standalone-chanlib.html` — `chanlib/` as the channel-adapter
  framework.

Each page answers: what does this component do; what are its
dependencies (none arizuko-internal); how is it wired into a
non-arizuko Go service; what's the minimal example. Gated behind
Phase 1 chrome migration shipping. Planned, not in progress.

## Open questions

1. **Version selector.** arizuko has versions (current v0.45.x). The
   reference pages aren't versioned today — they describe the
   current build. Do we surface a "you are reading the v0.45.4 docs"
   line in the breadcrumb area, or stay version-implicit and call
   out inline ("Available since v0.45.4") only where it matters?
   Recommend: version-implicit, inline-only. Lower noise. The
   footer-injected version stamp (see "Version visibility") covers
   the global question of "which build is this?".
2. **Code-tab convention.** Locked. Single-language captioned blocks;
   multi-shape commands as sequential blocks. No tab widget.
3. **Feedback widget ("Was this page helpful?").** dbt has one; we
   don't. The agent button is the feedback channel — a confused
   reader clicks Ask-the-agent and the question becomes the report.
4. **Source-link discipline.** Today's reference pages link each
   entry to `github.com/kronael/arizuko/blob/main/<path>#L<line>` —
   that's the grep-verifiability claim from `index.html` lede.
   Keep, but stop putting them in `<p class="src">` at the foot of
   every entry; fold into the Definition prose where the file is
   first named ("dispatch lives in `cmd/arizuko/main.go:54`").
5. **Last-updated generation.** Locked: pre-commit hook writes the
   page's git author-date into a `<time>` element in `.docs-footer`
   on edit. No build step, no manual stamp. Hook lives in
   `.pre-commit-config.yaml` as a local Python entry; ~15 lines.
   First-pass implementation can hand-stamp with `git log -1` output
   if the hook is deferred.

## Non-goals

- Docusaurus / React rewrite. We stay plain HTML + hub.css + hub.js.
- Custom static site generator beyond hub.css/hub.js.
- Abandoning hub.css palette, 2px corners, dense typography, or
  arizuko color twists.
- In-page search, hosted search index, or any lexical-search UX.
  Discovery is conversational via the agent button (see "Discovery is
  conversational, not lexical").
- Multi-language code-tab UI. dbt doesn't use it; we won't either.

## Acceptance for Phase 1

- All 9 reference pages converted to the new template.
- Three-pane layout works on ≥1200px viewport; single-column collapses
  cleanly below; sidebar drawer toggles via hub.js on narrow.
- Visual identity unchanged at the token level: palette (hub.css
  `--bg`, `--fg`, accent variables) and 2px corner radius unchanged
  in `hub.css`; body and code font stacks unchanged. Layout
  necessarily moves (three panes vs single-column today); the check
  is that no palette / corner / typography variable shifts, not that
  the page looks identical to before.
- `template/web/CLAUDE.md` "Style rules" section updated to require
  the new template (skeleton block from this spec) for any new page
  under `/reference/`.
- New CSS additions live in `hub.css`; per-page inline `<style>`
  blocks deleted.
- `hub.js` `buildTOC()` added; runs on pages with `.docs-toc`.
- Prev/next pager + edit-this-page + last-updated visible on every
  reference page.
- Deploy verified via the standard rsync workflow to krons; every
  page returns 200 and renders without console errors.
- Spot-check on three pages (one small leaf, one big catalogue, one
  index) that breadcrumb, TOC, pager, and footer all match the
  template skeleton.
