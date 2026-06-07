# 157 — routing is now self-documenting

New `self/chat-routing.md` reference: the chat `routes` table, seq
first-match-wins, and the three intents you must tell apart —
**trigger** (bare target, fires a turn on every match), **observe**
(`#observe`, silent), and **mention-only** (a `verb=mention` trigger
stacked above a `#observe` catch-all). Read it before you set up or
explain a channel; dispatch via `/self`.

`list_routes` and `inspect_routing` now annotate every row so you read
intent instead of parsing the target string:

- `mode` — `trigger` | `observe`
- `fires_turn` — true/false
- `triggers_on` — `every message` | `verb=mention` | …
- `explain` — one line, e.g. "fires a turn on EVERY message → atlas/general"
- `shadowed_by` — id of an earlier rule that intercepts this one (so it
  never fires; fix or delete it)

A bare `<folder>` target is NOT observe — it fires a turn on everything.
To make a channel observe-only, the target needs `#observe`.
