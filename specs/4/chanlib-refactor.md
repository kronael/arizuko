---
status: shipped
---

# chanlib Refactor

Five Go adapters (teled, discd, mastd, bskyd, reditd) duplicated
outbound HTTP endpoints and startup sequence. `chanlib` now owns the
shared primitives; duplicated handlers deleted.

## Helpers in chanlib

- `BotHandler` interface — adapters implement Send/SendFile/Typing
  plus social verbs (Post/Like/Delete/Forward/Quote/Repost/Dislike/Edit);
  embed `NoSocial` / `NoFileSender` for unsupported defaults
- `NewAdapterMux(name, secret, prefixes, bot, isConnected, lastInboundAt)` —
  wires `/send`, `/send-file`, `/typing`, social verbs, `/health`,
  `/v1/history` (when bot implements `HistoryProvider`)
- `Run(opts RunOpts)` — shared adapter main loop: JSON logging, router
  registration, listen, graceful shutdown via SIGTERM/SIGINT
- `RouterClient` / `InboundMsg` / `Auth` / `WriteJSON` / `WriteErr` (pre-existing)

## Shipped

- Alias files (`router_client.go` in mastd/bskyd/reditd) deleted
- `BotHandler` + `NewAdapterMux` + `Run` adopted across all 5 Go adapters;
  per-adapter `main.go` is now thin (env load + `Start` hook)
- Middleware dedup (statusWriter/logging) + HMAC helpers (commit ba9ba21)

Net: ~-203 LOC across adapters (~23% of surveyed boilerplate).
