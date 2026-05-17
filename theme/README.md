# theme

Shared CSS, theme-toggle JS, and minimal HTML page scaffolding for the
operator-facing web surfaces (`onbod`, `dashd`, `auth` login pages).

## Purpose

arizuko serves HTML from several Go daemons that all need to look the
same — onbod's onboarding flow, dashd's operator console, the auth/web
login + OAuth landing pages. Rather than ship a template engine, all of
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

## Dependencies

Standard library only (`html`, `html/template`).

## Files

- `theme.go` — `CSS`, `ThemeScript`, `ToggleScript`, `Head`, `Page`.
  Single source of theme variables; if a daemon hand-rolls colours,
  it has drifted and should be ported back here.

## Related

- `onbod/main.go`, `dashd/main.go`, `auth/web.go` — consumers
- `template/web/CLAUDE.md` — voice/style for operator-facing web docs
  (separate surface; that's the static site, this is the dynamic
  operator pages)
