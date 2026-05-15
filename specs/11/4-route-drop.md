---
status: spec
phase: next
---

# Route-Level Drop Primitive

## Problem

`routes` maps inbound JID patterns to a target folder. There is no way
to express "match this pattern but drop the message". Operators want a
firewall-style pattern: a guild-wildcard route catches mentions broadly,
then specific noisy channels are matched-and-discarded so they don't
pollute the folder context (and don't spawn a container turn).

Live case: a Discord guild has a PnL bot spamming a non-conversational
channel. Operator wants `@assistant` to work in any guild channel AND
the named channel to fire on every message AND the PnL channel ignored
entirely. Current workaround is a narrow whitelist — loses the
"any-guild-mention" property.

Today, an unmatched message simply isn't stored (gateway routing
returns ""). Drop should behave like "matched and intentionally
discarded": still no storage, but routing stops on first hit so later
allow routes don't catch it.

## Options

### (a) Sentinel target string

`target='/dev/null'` (or `target=''`) means drop. One branch added in
`router.ResolveRoute` (`router/router.go:326`): if matched route's
target equals the sentinel, return "".

- Pros: zero schema change; ~2 LOC.
- Cons: magic string overlaps the folder namespace; `SetRoutes` (which
  groups routes by folder prefix in `store/routes.go:48`) needs a
  carve-out; CLI/MCP must validate the sentinel as a reserved word.

### (b) New `effect` column

Migration adds `routes.effect TEXT NOT NULL DEFAULT 'allow'`.
`effect='drop'` rows match-and-discard. ~30 LOC: migration + scan
column + one branch in `ResolveRoute` + `core.Route` field +
CLI/MCP plumbing.

- Pros: mirrors `acl.effect` semantically (already shipped pattern in
  `specs/6/9-acl-unified.md`); no reserved strings in the target
  namespace; `SetRoutes` groups by folder regardless of effect;
  `target` stays meaningful on drop rows (documents which folder
  "would have" matched, useful when toggling drop → allow).
- Cons: one column + one migration + one field across the route I/O
  surface.

## Recommendation

**(b)**. Routes already mirror ACL in structure (seq, match, target);
mirroring `effect` keeps the two route-like tables shaped alike and
keeps the target column a pure folder reference. The magic-string
carve-outs needed by (a) cost more lines spread across more files than
one column costs in one place.

## SQL

```sql
-- store/migrations/00NN-routes-effect.sql
ALTER TABLE routes ADD COLUMN effect TEXT NOT NULL DEFAULT 'allow'
  CHECK (effect IN ('allow','drop'));
```

Existing rows backfill to `'allow'` via the default.

## Code sketch

`core.Route` gains `Effect string`. `store/routes.go` `routeCols`
includes `effect`. `router/router.go:326`:

```go
func ResolveRoute(msg core.Message, routes []core.Route) string {
    for _, r := range routes {
        if !RouteMatches(r, msg) {
            continue
        }
        if r.Effect == "drop" {
            return ""
        }
        t := expandTarget(r.Target, msg)
        if t != "" {
            return t
        }
    }
    return ""
}
```

Note the difference vs unmatched: drop returns on first hit; unmatched
falls through to the next route. That's the whole point.

## Precedence

Strict seq order. No "drop beats allow" override. Operators control
precedence via `seq` (lower wins), the same lever as today — adding a
second rule wouldn't be discoverable. The PnL-channel rule must sort
before the guild-wildcard rule:

```
seq=10 effect=drop  match='room=1325215385397629070'   target=sloth
seq=20 effect=allow match='room=sloth-named-channel'   target=sloth
seq=30 effect=allow match='guild=sloth_guild verb=@me' target=sloth
```

## Interaction with impulse_config

`effect='drop'` rows ignore `impulse_config` — the message never
reaches the agent, so there's nothing to impulse. `GetImpulseConfigJSON`
(`store/routes.go:100`) must skip drop rows during its scan. Storing
both is allowed (operator toggles drop → allow without rewriting
impulse) but the impulse only fires once `effect='allow'`.

## Test cases

- drop-then-allow (matched by drop, never reaches allow → "")
- allow-then-drop (matched by allow first, drop never consulted → folder)
- drop-with-glob (`room=spam-*` drops every spam channel)
- drop matching multiple later routes (first drop wins, no allow runs)
- drop row's `impulse_config` is ignored by `GetImpulseConfigJSON`
- unmatched-by-anything still returns "" (no regression)

## CLI

`arizuko route add --drop room=<pattern> --target <folder>` —
`--drop` sets `effect=drop`. Default omitted → `allow`.
`arizuko route list` shows an `EFFECT` column.

## MCP

`routes.add` accepts `effect: "allow" | "drop"` (default `"allow"`).
`routes.list` returns `effect` on each row.

## Open questions

- Should drop rows require a non-empty target, or accept empty? (Lean:
  require a folder — keeps the row scoped to a folder's route set so
  `SetRoutes` and `ListRoutes` group it correctly.)
- Does `arizuko route audit` surface dropped-message counts? Useful
  for "is this drop rule too aggressive?" — out of scope for v1.
- Should the gateway log dropped messages at INFO with `route_id`?
  Lean yes, single line, no body — for "why didn't my message land?"
  debugging.
- Do channel adapters need to know about drops (e.g. skip ACK)? Lean
  no — drop happens after ingestion, ACK already sent.
- Future: `effect='quarantine'` to store but not dispatch? Defer until
  a concrete operator ask arrives.
