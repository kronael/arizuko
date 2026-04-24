# theme

Shared CSS + HTML page scaffolding.

## Purpose

Two tiny helpers used by `onbod` and `dashd` to render consistent HTML
pages without pulling in a template engine.

## Public API

- `Head(title string) string` — `<head>` with shared styles
- `Page(title string, body template.HTML) string` — full HTML document

## Dependencies

None (stdlib only).

## Files

- `theme.go`
