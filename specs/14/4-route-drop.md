---
status: draft
phase: next
---

# Route-level drop primitive

`routes` maps inbound JID patterns to a target folder. There is no way to
say "match this pattern and drop it". Operators want the firewall shape: a
guild-wildcard route catches mentions broadly, then specific noisy channels
are matched-and-discarded so they neither pollute folder context nor spawn a
container turn.

Live case: a Discord guild has a PnL bot spamming a non-conversational
channel. The operator wants `@assistant` to work in any guild channel, the
named channel to fire on every message, **and** the PnL channel ignored.
The only workaround today is a narrow whitelist, which loses the
any-guild-mention property.

An unmatched message already isn't stored (`ResolveRoute` returns ""). Drop
differs by **stopping on first hit** so later allow routes don't catch it.
That difference is the whole feature.

## Decision: an `effect` column, not a sentinel target

The alternative was a magic target string (`target='/dev/null'`), one branch
in `ResolveRoute` and no migration — about two lines. Rejected:

- The sentinel overlaps the folder namespace, so `SetRoutes` (which groups
  routes by folder prefix) needs a carve-out and the CLI/MCP surface must
  validate a reserved word. Those carve-outs cost more lines, spread across
  more files, than one column costs in one place.
- Routes already mirror ACL structurally (seq, match, target). Mirroring
  `acl.effect` keeps the two route-like tables shaped alike — a shipped
  pattern ([5/32-acl-unified](../5/32-acl-unified.md)), not a new idea.
- `target` stays a pure folder reference, so a drop row still documents
  which folder _would have_ matched — which is what makes toggling
  drop → allow a one-field edit.

So: `ALTER TABLE routes ADD COLUMN effect TEXT NOT NULL DEFAULT 'allow'
CHECK (effect IN ('allow','drop'))`. Existing rows backfill via the default.
`ResolveRoute` returns "" immediately on a matching drop row instead of
falling through. `core.Route` gains the field; CLI gets `--drop` and an
`EFFECT` column; MCP `routes.add` accepts `effect`.

## Precedence is `seq`, with no override

Strict `seq` order, lowest wins. No "drop beats allow" rule — operators
already control precedence with `seq`, and a second, invisible precedence
mechanism would not be discoverable. The drop rule simply has to sort first.

## Drop rows ignore `impulse_config`

A dropped message never reaches the agent, so there is nothing to impulse —
`GetImpulseConfigJSON` must skip drop rows during its scan. Storing an
impulse on a drop row stays legal so the operator can toggle back without
rewriting it; it just doesn't fire until `effect='allow'`.

## Open

- Should a drop row require a non-empty target? Lean yes — it keeps the row
  inside a folder's route set so `SetRoutes`/`ListRoutes` group it correctly.
- Log dropped messages at INFO with the route id (no body)? Lean yes; it is
  the answer to "why didn't my message land?".
- `effect='quarantine'` (store but don't dispatch) — defer until an operator
  actually asks.
