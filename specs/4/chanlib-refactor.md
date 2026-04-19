---
status: shipped
---

# chanlib Refactor

Five Go adapters (teled, discd, mastd, bskyd, reditd) duplicated
outbound HTTP endpoints and startup sequence. `chanlib` now owns the
shared primitives; duplicated handlers deleted.

## Helpers in chanlib

- `ParseSend(w, r) *SendReq` — decode `/send` body, write 400 on error
- `ParseSendFile(w, r, prefix) *SendFileReq` — multipart + tempfile spill
- `NoopTyping` — `http.HandlerFunc` for adapters without typing
- `HealthHandler(name, prefixes)` — `GET /health`
- `RunAdapter(cfg, handler, start)` — shared startup/shutdown
- `RouterClient` / `InboundMsg` / `Auth` / `WriteJSON` / `WriteErr` (pre-existing)

## Shipped

- Alias files (`router_client.go` in mastd/bskyd/reditd) deleted
- `HealthHandler`, `NoopTyping`, `ParseSend`, `ParseSendFile`,
  `RunAdapter` adopted across all 5 Go adapters
- Middleware dedup (statusWriter/logging) + HMAC helpers (commit ba9ba21)

Net: ~-203 LOC across adapters (~23% of surveyed boilerplate).
