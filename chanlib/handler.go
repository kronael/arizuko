package chanlib

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type SendRequest struct {
	ChatJID  string `json:"chat_jid"`
	Content  string `json:"content"`
	ReplyTo  string `json:"reply_to"`
	ThreadID string `json:"thread_id"`
}

// BotHandler is the interface adapters implement for outbound messaging.
// Send returns the sent message ID (may be ""); Typing is fire-and-forget.
// Post/Like/Delete and Forward/Quote/Repost/Dislike/Edit are social
// primitives — adapters that don't support a verb should embed NoSocial
// to get *UnsupportedError defaults, or override with a per-platform
// Unsupported(...) carrying a concrete hint.
type BotHandler interface {
	Send(req SendRequest) (string, error)
	SendFile(jid, path, name, caption string) error
	Typing(jid string, on bool)
	Post(req PostRequest) (string, error)
	Like(req LikeRequest) error
	Delete(req DeleteRequest) error
	Forward(req ForwardRequest) (string, error)
	Quote(req QuoteRequest) (string, error)
	Repost(req RepostRequest) (string, error)
	Dislike(req DislikeRequest) error
	Edit(req EditRequest) error
}

type PostRequest struct {
	ChatJID    string   `json:"chat_jid"`
	Content    string   `json:"content"`
	MediaPaths []string `json:"media_paths,omitempty"`
}

type LikeRequest struct {
	ChatJID  string `json:"chat_jid"`
	TargetID string `json:"target_id"`
	Reaction string `json:"reaction"`
}

type DeleteRequest struct {
	ChatJID  string `json:"chat_jid"`
	TargetID string `json:"target_id"`
}

type ForwardRequest struct {
	SourceMsgID string `json:"source_msg_id"`
	TargetJID   string `json:"target_jid"`
	Comment     string `json:"comment,omitempty"`
}

type QuoteRequest struct {
	ChatJID     string `json:"chat_jid"`
	SourceMsgID string `json:"source_msg_id"`
	Comment     string `json:"comment"`
}

type RepostRequest struct {
	ChatJID     string `json:"chat_jid"`
	SourceMsgID string `json:"source_msg_id"`
}

type DislikeRequest struct {
	ChatJID  string `json:"chat_jid"`
	TargetID string `json:"target_id"`
}

type EditRequest struct {
	ChatJID  string `json:"chat_jid"`
	TargetID string `json:"target_id"`
	Content  string `json:"content"`
}

// ErrUnsupported marks a social action not implemented on this platform.
// Adapter HTTP layer maps this to 501.
var ErrUnsupported = errors.New("unsupported")

// UnsupportedError is the structured form of ErrUnsupported. When an
// adapter returns one of these (instead of a plain ErrUnsupported), the
// HTTP layer encodes Tool/Platform/Hint into the 501 body so the agent
// receives a concrete alternative. Is(target) reports true when target
// is ErrUnsupported, so legacy errors.Is(err, ErrUnsupported) checks
// keep working across the stack.
type UnsupportedError struct {
	Tool     string `json:"tool"`
	Platform string `json:"platform"`
	Hint     string `json:"hint"`
}

func (e *UnsupportedError) Error() string {
	if e == nil {
		return "unsupported"
	}
	if e.Hint == "" {
		return fmt.Sprintf("unsupported: %s on %s", e.Tool, e.Platform)
	}
	return fmt.Sprintf("unsupported: %s on %s: %s", e.Tool, e.Platform, e.Hint)
}

func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

// Unsupported builds a structured UnsupportedError. Adapters use it to
// teach the agent a concrete alternative for the (tool, platform) pair.
func Unsupported(tool, platform, hint string) error {
	return &UnsupportedError{Tool: tool, Platform: platform, Hint: hint}
}

// NoSocial is a zero-value mixin providing "unsupported" defaults for
// Post, Like, Delete. Adapters that implement a subset embed this
// and override the relevant method(s).
type NoSocial struct{}

func (NoSocial) Post(PostRequest) (string, error)       { return "", ErrUnsupported }
func (NoSocial) Like(LikeRequest) error                 { return ErrUnsupported }
func (NoSocial) Delete(DeleteRequest) error             { return ErrUnsupported }
func (NoSocial) Forward(ForwardRequest) (string, error) { return "", ErrUnsupported }
func (NoSocial) Quote(QuoteRequest) (string, error)     { return "", ErrUnsupported }
func (NoSocial) Repost(RepostRequest) (string, error)   { return "", ErrUnsupported }
func (NoSocial) Dislike(DislikeRequest) error           { return ErrUnsupported }
func (NoSocial) Edit(EditRequest) error                 { return ErrUnsupported }

// HistoryRequest is the query for platform-side history fetch.
// Before is RFC3339; empty means "latest". Limit is clamped by the adapter.
type HistoryRequest struct {
	ChatJID string
	Before  time.Time
	Limit   int
}

// HistoryResponse is returned by an adapter's /v1/history endpoint.
// Source is one of "platform", "platform-capped", "cache-only", "unsupported".
// Cap is a human-readable limit note ("24h", "1000") when Source is capped.
type HistoryResponse struct {
	Source   string       `json:"source"`
	Cap      string       `json:"cap,omitempty"`
	Messages []InboundMsg `json:"messages"`
}

// HistoryProvider is an optional bot capability. Adapters that can fetch
// history from the platform implement this; those that can't skip it and
// the gateway falls back to the local DB.
type HistoryProvider interface {
	FetchHistory(req HistoryRequest) (HistoryResponse, error)
}

type NoFileSender struct{}

func (NoFileSender) SendFile(_, _, _, _ string) error { return errSendFile }

var errSendFile = errors.New("send-file not supported")

// NewAdapterMux wires up the standard adapter HTTP surface.
// isConnected must report whether the adapter's live connection to the
// platform is up (bot API reachable, websocket open, streaming attached,
// IMAP IDLE active, or last-poll within tolerance). /health returns 503
// when it reports false so Docker HEALTHCHECK flips correctly. Adapters
// with no long-lived connection (pure pollers post-auth) pass a closure
// that returns true once auth succeeds.
// lastInboundAt returns the unix seconds of the most recent successful
// inbound delivery to the router. If the timestamp is older than the
// adapter's staleness threshold, /health returns 503 status:"stale".
func NewAdapterMux(name, secret string, prefixes []string, bot BotHandler, isConnected func() bool, lastInboundAt func() int64) *http.ServeMux {
	if isConnected == nil {
		panic("chanlib.NewAdapterMux: isConnected must not be nil")
	}
	if lastInboundAt == nil {
		panic("chanlib.NewAdapterMux: lastInboundAt must not be nil")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", Auth(secret, handleSend(bot)))
	mux.HandleFunc("POST /send-file", Auth(secret, handleSendFile(bot)))
	mux.HandleFunc("POST /typing", Auth(secret, handleTyping(bot)))
	mux.HandleFunc("POST /post", Auth(secret, handlePost(bot)))
	mux.HandleFunc("POST /like", Auth(secret, handleLike(bot)))
	mux.HandleFunc("POST /delete", Auth(secret, handleDelete(bot)))
	mux.HandleFunc("POST /forward", Auth(secret, handleForward(bot)))
	mux.HandleFunc("POST /quote", Auth(secret, handleQuote(bot)))
	mux.HandleFunc("POST /repost", Auth(secret, handleRepost(bot)))
	mux.HandleFunc("POST /dislike", Auth(secret, handleDislike(bot)))
	mux.HandleFunc("POST /edit", Auth(secret, handleEdit(bot)))
	mux.HandleFunc("GET /health", handleHealth(name, prefixes, isConnected, lastInboundAt))
	if hp, ok := bot.(HistoryProvider); ok {
		mux.HandleFunc("GET /v1/history", Auth(secret, handleHistory(hp)))
	}
	return mux
}

func handleHistory(hp HistoryProvider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		jid := q.Get("jid")
		if jid == "" {
			WriteErr(w, 400, "jid required")
			return
		}
		req := HistoryRequest{ChatJID: jid, Limit: 100}
		if s := q.Get("limit"); s != "" {
			if n, err := strconv.Atoi(s); err == nil && n > 0 {
				req.Limit = n
			}
		}
		if s := q.Get("before"); s != "" {
			t, err := time.Parse(time.RFC3339, s)
			if err != nil {
				WriteErr(w, 400, "invalid before (RFC3339)")
				return
			}
			req.Before = t
		}
		resp, err := hp.FetchHistory(req)
		if err != nil {
			WriteErr(w, 502, err.Error())
			return
		}
		if resp.Source == "" {
			resp.Source = "platform"
		}
		WriteJSON(w, resp)
	}
}

func handleSend(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req SendRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
			WriteErr(w, 400, "chat_jid and content required")
			return
		}
		id, err := bot.Send(req)
		if err != nil {
			WriteErr(w, 502, err.Error())
			return
		}
		resp := map[string]any{"ok": true}
		if id != "" {
			resp["id"] = id
		}
		WriteJSON(w, resp)
	}
}

func handleSendFile(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.ParseMultipartForm(50<<20) != nil {
			WriteErr(w, 400, "invalid multipart")
			return
		}
		jid, name, caption := r.FormValue("chat_jid"), r.FormValue("filename"), r.FormValue("caption")
		if jid == "" {
			WriteErr(w, 400, "chat_jid required")
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			WriteErr(w, 400, "file required")
			return
		}
		defer file.Close()
		if name == "" {
			name = hdr.Filename
		}
		// Strip directory components and reject traversal tokens.
		// `name` is attacker-controlled (multipart form value / filename
		// header); without this, filepath.Join(dir, name) can escape
		// the temp dir.
		name = filepath.Base(filepath.Clean(name))
		if name == "" || name == "." || name == ".." || strings.Contains(name, "..") || strings.ContainsRune(name, os.PathSeparator) {
			WriteErr(w, 400, "invalid filename")
			return
		}
		dir, err := os.MkdirTemp("", "chan-")
		if err != nil {
			WriteErr(w, 500, "temp dir failed")
			return
		}
		defer os.RemoveAll(dir)
		localPath := filepath.Join(dir, name)
		tmp, err := os.Create(localPath)
		if err != nil {
			WriteErr(w, 500, "temp file failed")
			return
		}
		io.Copy(tmp, file)
		tmp.Close()
		if err := bot.SendFile(jid, localPath, name, caption); err != nil {
			WriteErr(w, 502, err.Error())
			return
		}
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handleTyping(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req struct {
			ChatJID string `json:"chat_jid"`
			On      bool   `json:"on"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			slog.Warn("typing: decode failed", "err", err)
			WriteErr(w, 400, "invalid json body")
			return
		}
		if req.ChatJID == "" {
			WriteErr(w, 400, "chat_jid required")
			return
		}
		bot.Typing(req.ChatJID, req.On)
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handlePost(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req PostRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.Content == "" {
			WriteErr(w, 400, "chat_jid and content required")
			return
		}
		id, err := bot.Post(req)
		if err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		resp := map[string]any{"ok": true}
		if id != "" {
			resp["id"] = id
		}
		WriteJSON(w, resp)
	}
}

func handleLike(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req LikeRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.TargetID == "" {
			WriteErr(w, 400, "chat_jid and target_id required")
			return
		}
		if err := bot.Like(req); err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handleDelete(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req DeleteRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.TargetID == "" {
			WriteErr(w, 400, "chat_jid and target_id required")
			return
		}
		if err := bot.Delete(req); err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handleForward(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req ForwardRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.SourceMsgID == "" || req.TargetJID == "" {
			WriteErr(w, 400, "source_msg_id and target_jid required")
			return
		}
		id, err := bot.Forward(req)
		if err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		resp := map[string]any{"ok": true}
		if id != "" {
			resp["id"] = id
		}
		WriteJSON(w, resp)
	}
}

func handleQuote(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req QuoteRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.SourceMsgID == "" {
			WriteErr(w, 400, "chat_jid and source_msg_id required")
			return
		}
		id, err := bot.Quote(req)
		if err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		resp := map[string]any{"ok": true}
		if id != "" {
			resp["id"] = id
		}
		WriteJSON(w, resp)
	}
}

func handleRepost(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req RepostRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.SourceMsgID == "" {
			WriteErr(w, 400, "chat_jid and source_msg_id required")
			return
		}
		id, err := bot.Repost(req)
		if err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		resp := map[string]any{"ok": true}
		if id != "" {
			resp["id"] = id
		}
		WriteJSON(w, resp)
	}
}

func handleDislike(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req DislikeRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.TargetID == "" {
			WriteErr(w, 400, "chat_jid and target_id required")
			return
		}
		if err := bot.Dislike(req); err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handleEdit(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req EditRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" || req.TargetID == "" || req.Content == "" {
			WriteErr(w, 400, "chat_jid, target_id and content required")
			return
		}
		if err := bot.Edit(req); err != nil {
			if writeUnsupported(w, err) {
				return
			}
			WriteErr(w, 502, err.Error())
			return
		}
		WriteJSON(w, map[string]any{"ok": true})
	}
}

// writeUnsupported writes a 501 response when err is unsupported. If err
// carries structured *UnsupportedError, the body includes tool/platform/
// hint; otherwise plain {"ok":false,"error":"unsupported"}. Returns true
// when err was an unsupported variant and the response was written.
func writeUnsupported(w http.ResponseWriter, err error) bool {
	var ue *UnsupportedError
	if errors.As(err, &ue) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(501)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok":       false,
			"error":    "unsupported",
			"tool":     ue.Tool,
			"platform": ue.Platform,
			"hint":     ue.Hint,
		})
		return true
	}
	if errors.Is(err, ErrUnsupported) {
		WriteErr(w, 501, "unsupported")
		return true
	}
	return false
}

// staleThresholds sets per-adapter tolerance before /health flips to stale.
// Realtime streaming/long-poll adapters use 5m; email uses 10m because IDLE
// + poll-fallback is naturally lumpier. Reddit uses 1h because low-traffic
// subreddits may legitimately have no new submissions for long stretches
// while the poller is perfectly healthy (isConnected covers that).
// Adapters not listed fall back to the 5m default.
var staleThresholds = map[string]time.Duration{
	"email":  10 * time.Minute,
	"reddit": 60 * time.Minute,
}

const defaultStaleThreshold = 5 * time.Minute

func handleHealth(name string, prefixes []string, isConnected func() bool, lastInboundAt func() int64) http.HandlerFunc {
	threshold := defaultStaleThreshold
	if t, ok := staleThresholds[name]; ok {
		threshold = t
	}
	return func(w http.ResponseWriter, _ *http.Request) {
		status, code := "ok", http.StatusOK
		var staleSec int64
		switch {
		case !isConnected():
			status, code = "disconnected", http.StatusServiceUnavailable
		default:
			last := lastInboundAt()
			staleSec = time.Now().Unix() - last
			if last > 0 && staleSec > int64(threshold.Seconds()) {
				status, code = "stale", http.StatusServiceUnavailable
			}
		}
		resp := map[string]any{
			"status": status, "name": name, "jid_prefixes": prefixes,
			"last_inbound_at": lastInboundAt(),
		}
		if status == "stale" {
			resp["stale_seconds"] = staleSec
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
