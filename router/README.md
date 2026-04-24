# router

Message formatting + routing rule evaluation.

## Purpose

Pure functions: format message batches as XML for the agent, parse agent
output into user-visible chunks, evaluate route-table entries against
messages. No I/O, no state.

## Public API

- `FormatMessages(msgs []core.Message, observed ...[]core.Message) string` — XML batch for `<messages>`
- `FormatOutbound(raw string) string` — strip `<think>`, status blocks, fences
- `ExtractStatusBlocks(s) (string, []string)` — separate `<status>` lines
- `StripThinkBlocks(s) string` — strip `<think>…</think>`
- `UserContextXml(sender, groupDir) string` — per-user XML context
- `IsAuthorizedRoutingTarget(source, target) bool`
- `RouteMatches(r core.Route, msg core.Message) bool`
- `ResolveRoute(msg, routes) string` — first matching route's target

## Dependencies

- `core`

## Files

- `router.go`

## Related docs

- `ROUTING.md` — full rule syntax and examples
- `ARCHITECTURE.md` (Prompt Assembly)
