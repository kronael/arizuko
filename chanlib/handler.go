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
// Send returns the sent message ID (may be ""). Adapters that lack voice
// embed NoVoiceSender; those without social verbs embed NoSocial.
type BotHandler interface {
	Send(req SendRequest) (string, error)
	SendFile(jid, path, name, caption string) error
	SendVoice(jid, audioPath, caption string) (string, error)
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

// ErrUnsupported marks a social action not implemented on this platform (maps to 501).
var ErrUnsupported = errors.New("unsupported")

// UnsupportedError is the structured form. The HTTP layer encodes Tool/Platform/Hint
// so the agent receives a concrete alternative. Is(ErrUnsupported) returns true so
// errors.Is checks keep working across the stack.
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

func Unsupported(tool, platform, hint string) error {
	return &UnsupportedError{Tool: tool, Platform: platform, Hint: hint}
}

// NoSocial is a zero-value mixin with "unsupported" defaults for all social verbs.
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
// Source on the response is one of "platform", "platform-capped", "cache-only", "unsupported".
type HistoryRequest struct {
	ChatJID string
	Before  time.Time // zero means "latest"
	Limit   int       // clamped by adapter
}

type HistoryResponse struct {
	Source   string       `json:"source"`
	Cap      string       `json:"cap,omitempty"` // e.g. "24h", "1000" when source is capped
	Messages []InboundMsg `json:"messages"`
}

// HistoryProvider is an optional capability; adapters that omit it let the
// gateway fall back to the local DB.
type HistoryProvider interface {
	FetchHistory(req HistoryRequest) (HistoryResponse, error)
}

type NoFileSender struct{}

func (NoFileSender) SendFile(_, _, _, _ string) error {
	return Unsupported("send_file", "", "this adapter does not support file uploads")
}

type NoVoiceSender struct{}

func (NoVoiceSender) SendVoice(_, _, _ string) (string, error) {
	return "", ErrUnsupported
}

// NewAdapterMux wires up the standard adapter HTTP surface.
// isConnected: platform link is up (websocket/polling/IMAP IDLE); /health → 503 when false.
// lastInboundAt: unix seconds of last successful inbound delivery; /health → stale when old.
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
	mux.HandleFunc("POST /send-voice", Auth(secret, handleSendVoice(bot)))
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

// receiveUpload parses a multipart upload, sanitizes the filename, writes to
// a temp dir, and returns (jid, localPath, name, caption, cleanup). On any
// error it writes the HTTP response and returns ok=false. Caller must call
// cleanup() when done with the file.
//
// `name` is attacker-controlled (multipart form value / filename header);
// filepath.Clean + Base prevents path traversal out of the temp dir.
func receiveUpload(w http.ResponseWriter, r *http.Request, tmpPrefix string) (jid, localPath, name, caption string, cleanup func(), ok bool) {
	if r.ParseMultipartForm(50<<20) != nil {
		WriteErr(w, 400, "invalid multipart")
		return
	}
	jid, name, caption = r.FormValue("chat_jid"), r.FormValue("filename"), r.FormValue("caption")
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
	name = filepath.Base(filepath.Clean(name))
	if name == "" || name == "." || name == ".." || strings.Contains(name, "..") || strings.ContainsRune(name, os.PathSeparator) {
		WriteErr(w, 400, "invalid filename")
		return
	}
	dir, err := os.MkdirTemp("", tmpPrefix)
	if err != nil {
		WriteErr(w, 500, "temp dir failed")
		return
	}
	localPath = filepath.Join(dir, name)
	tmp, err := os.Create(localPath)
	if err != nil {
		os.RemoveAll(dir)
		WriteErr(w, 500, "temp file failed")
		return
	}
	_, copyErr := io.Copy(tmp, file)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		os.RemoveAll(dir)
		WriteErr(w, 500, "temp file write failed")
		return
	}
	return jid, localPath, name, caption, func() { os.RemoveAll(dir) }, true
}

func handleSendFile(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jid, localPath, name, caption, cleanup, ok := receiveUpload(w, r, "chan-")
		if !ok {
			return
		}
		defer cleanup()
		if err := bot.SendFile(jid, localPath, name, caption); err != nil {
			WriteErr(w, 502, err.Error())
			return
		}
		WriteJSON(w, map[string]any{"ok": true})
	}
}

func handleSendVoice(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jid, localPath, _, caption, cleanup, ok := receiveUpload(w, r, "chan-voice-")
		if !ok {
			return
		}
		defer cleanup()
		id, err := bot.SendVoice(jid, localPath, caption)
		writeBotResult(w, id, err)
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

func writeBotResult(w http.ResponseWriter, id string, err error) {
	if err == nil {
		resp := map[string]any{"ok": true}
		if id != "" {
			resp["id"] = id
		}
		WriteJSON(w, resp)
		return
	}
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
		return
	}
	if errors.Is(err, ErrUnsupported) {
		WriteErr(w, 501, "unsupported")
		return
	}
	WriteErr(w, 502, err.Error())
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
		writeBotResult(w, id, err)
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
		writeBotResult(w, "", bot.Like(req))
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
		writeBotResult(w, "", bot.Delete(req))
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
		writeBotResult(w, id, err)
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
		writeBotResult(w, id, err)
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
		writeBotResult(w, id, err)
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
		writeBotResult(w, "", bot.Dislike(req))
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
		writeBotResult(w, "", bot.Edit(req))
	}
}

// staleThresholds: /health flips to stale when lastInboundAt is older than this.
// email: IDLE+poll-fallback is lumpier. reddit: sparse subreddits can be quiet for hours.
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
		last := lastInboundAt()
		var staleSec int64
		switch {
		case !isConnected():
			status, code = "disconnected", http.StatusServiceUnavailable
		default:
			staleSec = time.Now().Unix() - last
			if last > 0 && staleSec > int64(threshold.Seconds()) {
				status = "stale"
			}
		}
		resp := map[string]any{
			"status": status, "name": name, "jid_prefixes": prefixes,
			"last_inbound_at": last,
		}
		if status == "stale" {
			resp["stale_seconds"] = staleSec
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}
