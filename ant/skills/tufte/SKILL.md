---
name: tufte
description: >
  Apply Edward Tufte's principles to produce, design, critique, or improve
  any data visualization — chart, graph, dashboard, KPI tile, sparkline,
  small multiple, time series, distribution, choropleth, infographic,
  table-with-data. Triggers on "make a chart", "visualize", "viz",
  "data viz", "chart", "graph", "dashboard", "infographic", "improve
  this chart", "design a dashboard", "Tufte", `/tufte`. Outputs
  self-contained HTML/SVG (no runtime deps) or React (Recharts + D3).
user-invocable: true
arg: <what to visualize, or a chart to critique>
---

<!-- CREDITS: adapted from github.com/aref-vc/tufte-claude-skill (MIT).
     Substantive content (principles, kill list, chart selection,
     checklist, presets, before/after gallery) unchanged. See LICENSE. -->

# Tufte — visual display, by the book

Turn "make me a chart" into a Tufte-compliant chart. Distilled from
*The Visual Display of Quantitative Information* (1983/2001),
*Envisioning Information* (1990), and *Visual Explanations* (1997).

## What this skill produces

Two output stacks, picked from project context:

1. **Self-contained HTML/SVG** — single file, inline SVG, no external
   deps. For one-offs, embeds, screenshots, chat replies, slide decks.
   This is the default for arizuko (agent reply is markdown/HTML, no
   build step).
2. **React (Recharts + D3 fallback)** — when the target project already
   uses React. Recharts where chart type fits; raw D3-in-React where it
   doesn't (slopegraph, sparkline-in-table, small multiples).

## How to use this skill

When invoked, work through these in order:

1. Read `principles.md` — 10 rules, one paragraph each. The whole frame.
2. Use `chart-selection.md` to pick the chart type from the user's data
   and goal. Don't reach for a chart you know; reach for the one this
   table prescribes.
3. Apply `kill-list.md` before rendering — strip what doesn't belong.
4. Open `before-after.html` when the user wants to see the difference.
   Six side-by-side examples covering the cases AI tools default to badly.
5. Run `checklist.md` before declaring the chart done. Twelve items,
   30 seconds.

For a quick lookup, `cheatsheet.html` is the one-page reference.

## Default behavior

Tufte rules apply by default. If the user explicitly requests something
on the kill list ("I need a pie chart for this board deck because the
CFO wants one"), comply, but note the Tufte alternative in a one-line
comment in the code or the response.

## The kill list, summarized

Not in any Tufte-compliant chart unless the user explicitly overrides:

- 3D effects on any 2D quantity
- Pie charts (use a sorted bar or a small table)
- Dual-axis charts (use two small multiples instead)
- Rainbow color scales for ordered data (use sequential single-hue)
- Heavy gridlines, frame boxes, tick marks at every minor unit
- Drop shadows, gradient fills, bevels, glow, "ducks"
- Legends placed away from data (label data directly)
- Moiré patterns, cross-hatching, dense stippling
- Redundant data-ink (bar plus number plus axis plus gridline all
  showing the same quantity)
- KPI cards with giant numbers and no context

## The keep list

These belong in most Tufte charts:

- Direct labels on data (no legends)
- Sparklines next to numbers
- Small multiples for any cross-cut
- Range frames (axes only span where data exists)
- Subtle gridlines (white-on-light, or omit)
- A single accent color, used to signal not to decorate
- Sorted categories (rarely alphabetical, almost never as-input)
- Tables when n ≤ ~20 and exact values matter

## File index

| File | Purpose |
|---|---|
| `principles.md` | Tufte's 10 rules with practical interpretation |
| `chart-selection.md` | Data + goal → chart type decision table |
| `kill-list.md` | What to remove from any chart |
| `checklist.md` | 12-item pre-publish check |
| `before-after.html` | Six side-by-side comparisons (open in browser) |
| `cheatsheet.html` | One-page reference |
| `presets/html-svg.md` | Style tokens + working SVG bar/line/sparkline/small-multiple |
| `presets/react.md` | Recharts theme + D3 patterns for slopegraph et al. |
| `LICENSE` | MIT (upstream: aref-vc/tufte-claude-skill) |

## arizuko notes

- Skill files live at `/workspace/self/ant/skills/tufte/` inside the
  agent container (mounted from the host image build); after the
  migrate skill seeds them, `~/.claude/skills/tufte/` on each group.
- Default output is a single self-contained `.html` file written to
  the group's `media/<YYYYMMDD>/` dir, then attached via `send_file`.
  No external CSS, no external JS, no font links — inline SVG only.
- Upstream large assets (gallery PNGs, cheatsheet PDF) were dropped
  to keep the skill light; see `github.com/aref-vc/tufte-claude-skill`
  for the full gallery and the printable PDF.
