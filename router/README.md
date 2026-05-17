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
   the ordered route table loaded by gated and return the resolved
   target folder.

No I/O (except `UserContextXml` reading `users/<id>.md` under the
group dir), no goroutines, no state. The route table itself is owned
by gated via `store/routes.go`; this package just walks rules someone
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
  — pull `<status>` lines out separately so gated can ship them to the
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

## Rule grammar (canonical: `ROUTING.md`)

`r.Match` is a space-separated list of `key=glob` predicates; every
predicate must match (AND). Predicates against unknown keys yield
empty values, which only match `key=`.

| key        | source                      |
| ---------- | --------------------------- |
| `platform` | `core.JidPlatform(ChatJID)` |
| `room`     | `core.JidRoom(ChatJID)`     |
| `chat_jid` | `msg.ChatJID`               |
| `sender`   | `msg.Sender`                |
| `verb`     | `msg.Verb`                  |

Glob semantics (`path.Match`, `*` does NOT cross `/`):

- `key=<exact>` — equality
- `key=<glob>` — glob match
- `key=*` — value non-empty
- `key=` — value empty/absent
- omit key — unconstrained

`r.Target` may contain `{sender}`, expanded to the canonical user
file id (`platform-short + "-" + ident`, e.g. `tg-12345`).

### Example

```
match: platform=telegram verb=mention
target: atlas/inbox
```

Matches any Telegram mention regardless of sender or room and routes
it into `atlas/inbox`. With `target: atlas/{sender}` the same rule
would route into a per-sender subfolder.

Full table semantics, precedence rules, and worked examples live in
`../ROUTING.md` — this README documents only what the Go package
exports.

## Where it's called

- `gateway/gateway.go` — per-message route resolution + prompt
  assembly for the agent batch
- `ipc/` — `<messages>` rendering when the in-container agent fetches
  message context over MCP
- gated's outbound path uses `FormatOutbound` / `ExtractStatusBlocks`
  to clean agent output before delivery

## Dependencies

- `core`

## Files

- `router.go` — all exports
- `router_test.go`

## Related docs

- `../ROUTING.md` — canonical rule grammar + table walkthrough
- `ARCHITECTURE.md` (Prompt Assembly, Routing)
- `../store/README.md` — owns the persistent `routes` table the
  evaluator walks
