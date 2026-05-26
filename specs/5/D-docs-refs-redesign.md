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
> patterns from external references (Divio four-category, Stripe three-
> column, dbt's reference-page rhythm cited 2026-05-25) but do NOT adopt
> their visuals. The hub.css palette, 2px corners, dense typography, and
> arizuko color twists stay. The job of an external reference is to
> inform structure and tone; the look is ours.

Phase 1 (reference/ — 9 pages) in detail; Phase 2 (concepts/, howto/,
examples/) sketched. Divio four-category split is existing and aligns
with dbt's `/reference/`, `/docs/`, `/guides/` namespacing — not in
scope to revisit.

## Sources

- Research: `/srv/data/arizuko_krons/groups/krons/facts/dbt-docs-design-study.md`
  (21KB, 7 dbt URLs, IA observations + content patterns + per-page-type
  templates + voice/tone + positioning). Authoritative reference for
  every dbt observation cited below.
- Current refs: `template/web/pub/arizuko/reference/*.html` (9 pages:
  `cli`, `env`, `grants`, `index`, `jid`, `mcp`, `schema`, `stats`,
  `tokens`, `topics`)
- Style guide: `template/web/CLAUDE.md` — "Voice", "Style rules"
- Visual-identity guard: root `CLAUDE.md` "Updating the web docs"
  (commit `4c93c49`)

## What we adopt from dbt

1. **Three-pane layout at wide viewport.** Left: section tree (Reference
   / Concepts / How-To / Examples, current section expanded). Middle:
   content, capped width. Right: page-internal TOC built from H2/H3.
   Breakpoint ≥1200px three-pane; <1200px single column, sidebar in a
   drawer, right TOC absent.
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
- **"Copy page" / AI-handoff bar** — novel UX experiment, defer.
- **Version-selector dropdown in top bar** — one current site; dropdown
  reading "v(current)" is noise. Open question below.
- **Hosted search (Algolia)** — ~15 pages, browser Ctrl-F suffices.
- **Tabbed code-block widget** — adds JS and divergence surface;
  sequential captioned blocks stay static-HTML.
- **Estimated reading time, time-to-read, last-viewed-by** — noise.
- **Marketing-footer block** — our footer stays minimal: repo link,
  changelog link, theme toggle.
- **Hero/marketing voice under `/reference/`** — reference is for the
  reader who has decided to use the thing.

## Three-pane layout

Implementation lives in `hub.css` + `hub.js`. No new framework, no build
step, plain HTML. The current per-page inline `<style>` blocks get
deleted — every page-level chrome rule moves into `hub.css`.

```
┌──────────────────────────────────────────────────────────────────────┐
│ top bar — arizuko wordmark · search (later) · theme toggle           │
├──────────┬───────────────────────────────────────┬───────────────────┤
│ nav      │ breadcrumb                            │ on this page      │
│          │ H1                                    │  • Definition     │
│ Reference│ definitional first sentence           │  • Usage          │
│  cli     │                                       │  • Examples       │
│  env     │ H2 Definition                         │  • Troubleshoot   │
│  grants  │   prose, code blocks, inline links    │                   │
│  …       │ H2 Usage                              │                   │
│ Concepts │   …                                   │                   │
│ How-To   │                                       │                   │
│ Examples │ feedback widget (defer)               │                   │
│          │ [← prev] · [next →]                   │                   │
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
Concepts                   (placeholder — links to concepts/index.html)
How-To                     (placeholder — links to howto/index.html)
Examples                   (placeholder — links to examples/index.html)
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

`concepts/`, `howto/`, `examples/` inherit the same three-pane layout
and the same chrome (breadcrumb, prev/next, edit, last-updated). Page-
type templates from the research:

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

Each Phase 2 page-type inherits Phase 1's three-pane chrome and the
nav tree; the body H2 rhythm is what differs.

## Open questions

1. **Search UX.** Skip for v1. Browser Ctrl-F covers ~15 pages. If
   the catalogue grows past ~30 pages, revisit with a JSON index
   built at copy-to-pub time + a `hub.js` page-internal filter.
2. **Version selector.** arizuko has versions (current v0.45.x). The
   reference pages aren't versioned today — they describe the
   current build. Do we surface a "you are reading the v0.45.4 docs"
   line in the breadcrumb area, or stay version-implicit and call
   out inline ("Available since v0.45.4") only where it matters?
   Recommend: version-implicit, inline-only. Lower noise.
3. **Code-tab convention.** Locked. Single-language captioned blocks;
   multi-shape commands as sequential blocks. No tab widget.
4. **Feedback widget ("Was this page helpful?").** dbt has one; we
   don't yet. Defer to Phase 2 — it needs a backend endpoint and an
   anti-spam story.
5. **Source-link discipline.** Today's reference pages link each
   entry to `github.com/kronael/arizuko/blob/main/<path>#L<line>` —
   that's the grep-verifiability claim from `index.html` lede.
   Keep, but stop putting them in `<p class="src">` at the foot of
   every entry; fold into the Definition prose where the file is
   first named ("dispatch lives in `cmd/arizuko/main.go:54`").
6. **Last-updated generation.** Locked: pre-commit hook writes the
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
- Abandoning the Divio four-category split (concepts / how-to /
  reference / examples) — it's existing and aligns with dbt.
- Hosted search (Algolia, Meilisearch). Browser Ctrl-F is enough.
- Multi-language code-tab UI. dbt doesn't use it; we won't either.
- AI-handoff utilities (copy-page) for v1.

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
