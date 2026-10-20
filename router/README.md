# router

Pure functions for prompt assembly and route-rule evaluation.

## Purpose

Two concerns, deliberately one package because they share message and
route data shapes:

1. **Prompt assembly** — render a batch of `core.Message` values as the
   `<messages>` XML the agent sees, strip `<think>`/`<status>`/
   `<internal>` blocks from agent output, materialise per-user context
   from `users/<id>.md` frontmatter.
2. **Route evaluation** — match a single inbound `core.Message` against
   the ordered route table loaded by routd and return the resolved
   target folder.

No I/O (except `UserContextXml` reading `users/<id>.md` under the
group dir), no goroutines, no state. The route table itself is owned
by routd via `routd/routes_http.go`; this package just walks rules someone
else loaded.

## Public API

### Prompt assembly

- `FormatMessages(msgs []core.Message, observed ...[]core.Message) string`
  — render an interleaved `<messages>…</messages>` block. Primary msgs
  tag as `<message>`, ambient siblings (passed as `observed`) tag as
  `<observed>`; both are sorted by timestamp so the agent sees true
  chronology. Emits `<reply-to>` sibling headers for messages with
  `ReplyToID`.
- `FormatOutbound(raw string) string` — strip `<internal>`,
  `<think>`, and `<status>` blocks; trim.
- `ExtractStatusBlocks(s string) (cleaned string, statuses []string)`
  — pull `<status>` lines out separately so routd can ship them to the
  operator surface without showing them to users.
- `StripThinkBlocks(s string) string` — depth-aware stripper for
  nested `<think>`.
- `UserContextXml(sender, groupDir string) string` — `<user
id=… name=… memory="~/users/<id>"/>` element, derived from the
  sender's `users/<id>.md` `name:` frontmatter. Path-traversal-guarded.

### Route evaluation

- `RouteMatches(r core.Route, msg core.Message) bool` — predicate.
- `ResolveRoute(msg core.Message, routes []core.Route) string` —
  walks `routes` in order, returns the first matching `Target` after
  `{sender}` expansion. Empty string when nothing matches.
- `ResolveRouteTarget(msg, routes) core.RouteTarget` — same, but
  parses the target fragment into `{Folder, Mode}`.
- `IsAuthorizedRoutingTarget(source, target string) bool` — enforces
  that a non-`root/*` source can only route into its own direct
  children.

## Rule grammar

`r.Match` is a space-separated list of `key=glob` predicates (AND);
`r.Target` may contain `{sender}`, expanded to the canonical user
file id. Grammar canonically in `../ROUTING.md` — match keys, glob
semantics, precedence.

## Where it's called

- `routd/loop.go` — per-message route resolution + prompt
  assembly for the agent batch
- `ipc/` — `<messages>` rendering when the in-container agent fetches
  message context over MCP
- routd's outbound path uses `FormatOutbound` / `ExtractStatusBlocks`
  to clean agent output before delivery

## Dependencies

- `core`

## Files

- `router.go` — all exports
- `router_test.go`

## Related docs

- `../ROUTING.md` — canonical rule grammar + table walkthrough
- `routd/README.md` § Prompt assembly
- `../store/README.md` — owns the persistent `routes` table the
  evaluator walks
