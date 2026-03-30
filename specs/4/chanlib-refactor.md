# Inter-service Communication Simplification

## Overview

Five Go adapters (teled, discd, mastd, bskyd, reditd) each implement the same
outbound HTTP endpoints and startup sequence. The patterns are nearly identical
but live in separate files. chanlib already provides the inbound primitives
(`RouterClient`, `InboundMsg`, `Auth`, `WriteJSON`, `WriteErr`). The outbound
side is not abstracted at all.

Total adapter server+main LOC surveyed: ~879 lines across 13 files.
Estimated reduction from all changes below: ~220 lines (~25%).

---

## 1. `handleSend` — identical in all 5 adapters

### Current state

Every adapter's `server.go` has the same 12-line handler:

```go
var req struct { ChatJID, Content, ReplyTo, ThreadID string }
if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
    chanlib.WriteErr(w, 400, "chat_jid and content required")
    return
}
// delegate to platform-specific send(...)
```

teled and discd have it (both ~12 LOC). mastd, bskyd, reditd have it (each
~11 LOC). Same decode + same validation + same error message, 5 times.

### Proposed simplification

Add to chanlib:

```go
type SendReq struct {
    ChatJID  string `json:"chat_jid"`
    Content  string `json:"content"`
    ReplyTo  string `json:"reply_to"`
    ThreadID string `json:"thread_id"`
}

// ParseSend decodes a /send body and writes 400 on error; returns nil on failure.
func ParseSend(w http.ResponseWriter, r *http.Request) *SendReq
```

Each adapter's handleSend becomes:

```go
req := chanlib.ParseSend(w, r)
if req == nil { return }
```

Estimated reduction: ~35 lines removed across 5 adapters, +8 lines in chanlib.
Net: **-27 lines**.

---

## 2. `handleSendFile` — identical in teled and discd

### Current state

teled/server.go L51–83 and discd/server.go L54–86 are identical except for
the temp file prefix (`tg-*` vs `dc-*`). Both do:

- `ParseMultipartForm(50<<20)` with same error path
- `FormValue("chat_jid")`, `FormValue("filename")`, `FormValue("caption")`
- `FormFile("file")` with same error path
- `os.CreateTemp` + `io.Copy` + `tmp.Close()` + `defer os.Remove`
- delegate to `bot.sendFile(jid, tmp.Name(), name, caption)`

~30 lines duplicated in both.

### Proposed simplification

Add to chanlib:

```go
type SendFileReq struct {
    ChatJID string
    Name    string
    Caption string
    TmpPath string // caller defers os.Remove(TmpPath)
}

// ParseSendFile parses multipart, spills file to a temp file, returns req.
// Writes 4xx on error. Caller must defer os.Remove(req.TmpPath) on success.
// prefix is passed through to os.CreateTemp (e.g. "tg-*", "dc-*").
func ParseSendFile(w http.ResponseWriter, r *http.Request, prefix string) *SendFileReq
```

Removes ~25 LOC from each of teled and discd. Adds ~30 LOC to chanlib.
Net: **-20 lines**. (Only two adapters currently have send_file; benefit grows
as emaid and others add it.)

---

## 3. `handleTyping` — identical in all 5 adapters

### Current state

mastd, bskyd, reditd have a no-op typing handler (3 LOC each):

```go
func (s *server) handleTyping(w http.ResponseWriter, _ *http.Request) {
    chanlib.WriteJSON(w, map[string]any{"ok": true})
}
```

teled and discd have real typing handlers (~8 LOC each) that delegate to the
platform. The no-op version is pure boilerplate.

### Proposed simplification

Add to chanlib:

```go
// NoopTyping is an http.HandlerFunc for adapters that don't support typing.
var NoopTyping = func(w http.ResponseWriter, _ *http.Request) {
    WriteJSON(w, map[string]any{"ok": true})
}
```

mastd, bskyd, reditd replace their handler method with one field assignment in
`handler()`. Each saves ~5 LOC.

Net: **-12 lines** (3 adapters × 4 lines saved).

---

## 4. `handleHealth` — identical in all 5 adapters

### Current state

Every adapter has the same 5-line health handler differing only in
`"jid_prefixes"` value:

```go
func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
    chanlib.WriteJSON(w, map[string]any{
        "status": "ok", "name": s.cfg.Name,
        "jid_prefixes": []string{"platform:"},
    })
}
```

### Proposed simplification

Add to chanlib:

```go
// HealthHandler returns an http.HandlerFunc for GET /health.
func HealthHandler(name string, prefixes []string) http.HandlerFunc
```

Each adapter replaces its handleHealth method (5 LOC) with a one-line
registration in `handler()`:

```go
mux.HandleFunc("GET /health", chanlib.HealthHandler(s.cfg.Name, s.cfg.JIDPrefixes))
```

This also pushes `jid_prefixes` up to the config struct, which is already where
`Name` lives. Net: **-20 lines** (5 adapters × 4 LOC saved, +0 in chanlib since
the implementation is trivially smaller than what it replaces).

---

## 5. `router_client.go` alias files — pure noise

### Current state

mastd, bskyd, reditd each have an 8-line file that is only type aliases and a
constructor forwarding to chanlib:

```go
type routerClient = chanlib.RouterClient
type inboundMsg = chanlib.InboundMsg
func newRouterClient(...) *routerClient { return chanlib.NewRouterClient(...) }
```

teled and discd import chanlib directly — no alias file.

### Proposed simplification

Delete the 3 alias files. Update mastd, bskyd, reditd to use `chanlib.RouterClient`
and `chanlib.InboundMsg` directly (same pattern as teled/discd). The alias was
likely a migration artifact.

Net: **-24 lines** (3 files × 8 lines).

---

## 6. `main()` startup boilerplate — ~60% identical across 5 adapters

### Current state

Every main() does:

1. Set up JSON slog logger (4 lines, identical)
2. Warn if `CHANNEL_SECRET` empty (3 lines, identical)
3. `signal.NotifyContext(SIGTERM, SIGINT)` (3 lines, identical)
4. `rc.Register(...)` with error check (6 lines, identical pattern)
5. `net.Listen` + `http.Server` + `go srv.Serve` (7 lines, identical pattern)
6. `<-ctx.Done()` + deregister + `srv.Close()` (4 lines, identical)

Adapter-specific: platform client init, optional bot.start(), platform-specific
capabilities, platform-specific deregister ordering.

### Proposed simplification

Add to chanlib:

```go
type AdapterConfig struct {
    Name, RouterURL, ChannelSecret, ListenAddr, ListenURL string
    JIDPrefixes []string
    Capabilities map[string]bool
}

// RunAdapter handles the shared startup/shutdown lifecycle.
// start(rc) is called after registration; start returns a stop func.
// handler is the mux to serve.
func RunAdapter(cfg AdapterConfig, handler http.Handler,
    start func(rc *RouterClient) (stop func(), err error))
```

Each main() shrinks from ~55 LOC to ~20 LOC (platform init + RunAdapter call).
chanlib gains ~35 LOC for the helper.

Net: **-100 lines** (5 adapters × ~20 LOC saved each, +35 in chanlib).

This is the highest-value change but requires each adapter to conform to the
`start` callback shape. teled/discd are straightforward; mastd/bskyd/reditd
are already simpler (no bot.start). The slog init and signal handling become
centrally correct for all adapters.

---

## Implementation order (easiest → highest impact)

| #   | Change             | Files touched           | Net LOC delta |
| --- | ------------------ | ----------------------- | ------------- |
| 1   | Delete alias files | mastd, bskyd, reditd    | -24           |
| 2   | HealthHandler      | chanlib + 5 servers     | -20           |
| 3   | NoopTyping         | chanlib + 3 servers     | -12           |
| 4   | ParseSend          | chanlib + 5 servers     | -27           |
| 5   | ParseSendFile      | chanlib + teled + discd | -20           |
| 6   | RunAdapter startup | chanlib + 5 mains       | -100          |

---

## Total estimated LOC reduction

| Change        | Delta    |
| ------------- | -------- |
| alias files   | -24      |
| HealthHandler | -20      |
| NoopTyping    | -12      |
| ParseSend     | -27      |
| ParseSendFile | -20      |
| RunAdapter    | -100     |
| **Total**     | **-203** |

203 lines removed from ~879 lines surveyed = ~23% reduction in adapter
boilerplate. chanlib grows by ~75 lines but those lines replace ~278 duplicated
lines across adapters — net deletion is real.

---

## Non-changes (deliberate)

- `handleSend` in reditd dispatches `comment` vs `submit` based on ReplyTo — the
  delegation logic is platform-specific. `ParseSend` decouples parsing from
  dispatch; the dispatch stays in reditd.
- `handleFile` in teled is Telegram-specific (two-step getFile + CDN proxy).
  No parallel in other adapters yet. Leave it.
- `WritJSON`/`WriteErr` in api/api.go are private copies of chanlib functions.
  api is an internal gated package; chanlib is for external adapters. Keep
  separate to avoid cross-boundary coupling.
- InboundAttachment and InboundMsg already live in chanlib — no change needed.
