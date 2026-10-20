# theme

Shared CSS, theme-toggle JS, and minimal HTML page scaffolding for every
HTML page a Go daemon serves.

## Purpose

arizuko serves HTML from several Go daemons that all need to look the
same: the login and OAuth pages in `auth`, onbod's onboarding flow, dashd's
console, and the `/dash/<daemon>/` page of `authd`, `onbod`, `proxyd`,
`routd`, `runed`, `timed`, `ttsd`, `webd` and every Go channel adapter
through `chanlib`. Rather than ship a template engine, all of
them pull a single CSS blob + a one-shot page wrapper from this
package. Dark/light theming, layout primitives (`page-center`,
`page-wide`, `cols`), card/button/table/form styles, and operator-grid
tiles are all defined once here.

No template engine. Pages are string-concatenated; callers escape user
input themselves (`html.EscapeString`) before wrapping with
`template.HTML(...)` and passing to `Page`.

## Public API

- `Head(title string) string` — `<head>` with charset, viewport,
  title (escaped + `arizuko — ` prefix), inlined `<style>{CSS}</style>`,
  and the pre-body `ThemeScript` so the saved theme is applied before
  first paint
- `Page(title string, body template.HTML) string` — full HTML document.
  Wraps `body` in a centered single-card layout (`page-center` +
  `card-md`) with the `arizuko` brand label and an escaped `<h2>` of
  the title. Use for the simple single-card screens (login, onboarding
  steps, confirmation pages).
- `CSS` — the raw stylesheet, exposed so callers building richer pages
  (dashd's multi-column console) can inline it inside their own
  `<style>` block without going through `Head`
- `ThemeScript` — inline `<script>` that reads `localStorage.hub-theme`
  (falling back to `prefers-color-scheme`) and sets
  `data-theme=dark|light` on `<html>` **before** body renders.
  Prevents flash-of-wrong-theme
- `ToggleScript` — `<script>` that wires `window.toggleTheme` to a
  `button.theme-toggle` element and renders the moon/sun glyph based
  on current theme. Include once at end of body when a page exposes
  the toggle button

## When to use which

- Single-card login/confirmation page → call `Page(title, body)` and
  you're done.
- Multi-section dashboard with custom layout → write the HTML
  yourself; inline `Head(title)` then `<body>...</body>` and append
  `ToggleScript` if you render the toggle button.

## The cockpit convention

The hub is tiles and a daemon is tables. dashd's `/dash/services/` renders
one tile per daemon with a status dot. A daemon's own `/dash/<daemon>/`
renders its state as tables, with at most a strip of count tiles at the
top, through `Page`. The daemon pages open with the crumb
`<div class="crumb"><a href="/dash/services/">arizuko hub</a> › <daemon></div>`,
so every page leads back to the hub. `routd`, `runed`, `authd`, `proxyd`,
`webd`, `ttsd` and the `chanlib` adapter page render it; `onbod` and
`timed` do not. The stylesheet defines `.crumbs`, the class dashd's own
pages use, and no rule for `.crumb`.

## Dependencies

Standard library only (`html`, `html/template`).

## Files

- `theme.go` — `CSS`, `ThemeScript`, `ToggleScript`, `Head`, `Page`.
  Single source of theme variables; if a daemon hand-rolls colours,
  it has drifted and should be ported back here.

## Related

- `auth/web.go`, `dashd/main.go`, `onbod/main.go`, `chanlib/dash.go` and
  each daemon's `dash.go` — consumers
- `template/web/CLAUDE.md` — voice/style for operator-facing web docs
  (separate surface; that's the static site, this is the dynamic
  operator pages)
