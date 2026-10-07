package chanlib

import (
	"context"
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

	"github.com/kronael/arizuko/chanlib/chanstore"
)

type SendRequest struct {
	ChatJID  string `json:"chat_jid"`
	Content  string `json:"content"`
	ReplyTo  string `json:"reply_to"`
	ThreadID string `json:"thread_id"`
	// ThreadRoot roots a NEW thread on that platform message when ThreadID is
	// empty; adapters without threading ignore it.
	ThreadRoot string `json:"thread_root"`
	// IdempotencyKey is routd's stable id for this delivery. routd re-sends a
	// `pending` row, so the SAME key arrives more than once by design; the mux
	// enforces the dedup (dedup.go). Empty = no dedup (SendVia, local dev).
	IdempotencyKey string `json:"idempotency_key"`
	// Options are pickable answers; only an adapter with the `buttons`
	// capability receives them, chanreg folds them into the text otherwise.
	Options []Option `json:"options,omitempty"`
}

// Option is one pickable answer. Data is what a press SENDS: the adapter
// delivers the press as an ordinary InboundMsg whose Content is that string,
// so a press and a typed command are one inbound path under one gate (spec
// 5/19); an adapter never interprets an option.
type Option struct {
	Label string `json:"label"`
	Data  string `json:"data"`
}

// Send is what an Option puts on the wire: Data when set, else the label.
func (o Option) Send() string {
	if o.Data != "" {
		return o.Data
	}
	return o.Label
}

// BotHandler is the interface adapters implement for outbound messaging.
// Send returns the sent message ID (may be ""). Adapters that lack voice
// embed NoVoiceSender; those without social verbs embed NoSocial.
type BotHandler interface {
	Send(req SendRequest) (string, error)
	SendFile(jid, path, name, caption, replyTo, threadID string) (string, error)
	SendVoice(jid, audioPath, caption, threadID string) (string, error)
	Typing(jid string, on bool)
	Post(req PostRequest) (string, error)
	Like(req LikeRequest) error
	Delete(req DeleteRequest) error
	Forward(req ForwardRequest) (string, error)
	Quote(req QuoteRequest) (string, error)
	Repost(req RepostRequest) (string, error)
	Dislike(req DislikeRequest) error
	Edit(req EditRequest) error
	Pin(req PinRequest) error
	Unpin(req UnpinRequest) error
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

type PinRequest struct {
	ChatJID  string `json:"chat_jid"`
	TargetID string `json:"target_id"`
}

// UnpinRequest unpins one message (TargetID set) or all pins (All=true).
type UnpinRequest struct {
	ChatJID  string `json:"chat_jid"`
	TargetID string `json:"target_id,omitempty"`
	All      bool   `json:"all,omitempty"`
}

// ErrUnsupported marks a social action not implemented on this platform (maps to 501).
var ErrUnsupported = errors.New("unsupported")

// ErrInvalidRequest marks a caller-input error (400), so the agent learns its
// input was wrong instead of seeing a 502 "upstream failed".
var ErrInvalidRequest = errors.New("invalid request")

// ErrPermanent marks a delivery failure a retry cannot repair; routd
// dead-letters the row at once. It lives here so the MCP wall (ipc)
// classifies by the SAME sentinel without importing chanreg.
var ErrPermanent = errors.New("permanent")

// ErrNotDelivered marks a TRANSIENT failure that PROVES the platform accepted
// nothing (a 429, an `ok:false`). deliverOnce releases the dedup claim on it:
// without this class every adapter-side rate limit left the claim `inflight`,
// a guaranteed dead-letter.
var ErrNotDelivered = errors.New("not delivered")

// UnsupportedError is the structured form; the HTTP layer encodes
// Tool/Platform/Hint so the agent receives a concrete alternative.
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
func (NoSocial) Pin(PinRequest) error                   { return ErrUnsupported }
func (NoSocial) Unpin(UnpinRequest) error               { return ErrUnsupported }

// gatedVerbProbes maps each capability key chanreg gates on to a probe of the
// verb; `fwd` gates Forward and `pin` gates both Pin and Unpin. `like`/`post`
// are NOT gated, so they are omitted.
var gatedVerbProbes = map[string]func(BotHandler) error{
	// A .png name so adapters that only accept image blobs (bskyd) take their
	// real branch instead of an extension-default Unsupported.
	"send_file":  func(b BotHandler) error { _, e := b.SendFile("", "x.png", "x.png", "", "", ""); return e },
	"send_voice": func(b BotHandler) error { _, e := b.SendVoice("", "", "", ""); return e },
	"delete":     func(b BotHandler) error { return b.Delete(DeleteRequest{}) },
	"edit":       func(b BotHandler) error { return b.Edit(EditRequest{}) },
	"pin":        func(b BotHandler) error { return b.Pin(PinRequest{}) },
	// Plausibly-formatted ids so adapters that validate input shape (teled
	// forward needs "<jid>|<id>") take their real branch, not a stub-like refusal.
	"fwd": func(b BotHandler) error {
		_, e := b.Forward(ForwardRequest{SourceMsgID: "x|1", TargetJID: "x"})
		return e
	},
	"quote":         func(b BotHandler) error { _, e := b.Quote(QuoteRequest{}); return e },
	"repost":        func(b BotHandler) error { _, e := b.Repost(RepostRequest{}); return e },
	"dislike":       func(b BotHandler) error { return b.Dislike(DislikeRequest{}) },
	"fetch_history": func(b BotHandler) error { return probeHistory(b) },
}

func probeHistory(b BotHandler) error {
	hp, ok := b.(HistoryProvider)
	if !ok {
		return ErrUnsupported // no FetchHistory method ⇒ effectively unsupported
	}
	_, e := hp.FetchHistory(HistoryRequest{})
	return e
}

// isStubVerb reports whether a verb is an unconditional Unsupported stub: a
// stub returns ErrUnsupported without a field deref, a real impl derefs a
// platform client and panics on a zero-value handler.
func isStubVerb(b BotHandler, probe func(BotHandler) error) (stub bool) {
	defer func() {
		if r := recover(); r != nil {
			stub = false // touched a nil client ⇒ real impl
		}
	}()
	return errors.Is(probe(b), ErrUnsupported)
}

// CapImplReport returns the drift between an adapter's advertised capability
// map and its verb implementations, probed on a zero-value handler. Empty
// means consistent.
func CapImplReport(b BotHandler, caps map[string]bool) []string {
	var out []string
	for cap, probe := range gatedVerbProbes {
		stub := isStubVerb(b, probe)
		advertised := caps[cap]
		switch {
		case advertised && stub:
			out = append(out, fmt.Sprintf("%s: advertised but verb returns Unsupported", cap))
		case !advertised && !stub:
			out = append(out, fmt.Sprintf("%s: verb implemented but not advertised", cap))
		}
	}
	return out
}

// NoPinSupport is a mixin for adapters whose platform lacks message pinning.
type NoPinSupport struct{}

func (NoPinSupport) Pin(PinRequest) error     { return ErrUnsupported }
func (NoPinSupport) Unpin(UnpinRequest) error { return ErrUnsupported }

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

func (NoFileSender) SendFile(_, _, _, _, _, _ string) (string, error) {
	return "", Unsupported("send_file", "", "this adapter does not support file uploads")
}

type NoVoiceSender struct{}

func (NoVoiceSender) SendVoice(_, _, _, _ string) (string, error) {
	return "", ErrUnsupported
}

// NewAdapterMux wires up the standard adapter HTTP surface.
// isConnected: platform link is up (websocket/polling/IMAP IDLE); /health → 503 when false.
// lastInboundAt: unix seconds of last successful inbound delivery; /health → stale when old.
func NewAdapterMux(name string, prefixes []string, bot BotHandler, isConnected func() bool, lastInboundAt func() int64, st *chanstore.Store) *http.ServeMux {
	if isConnected == nil {
		panic("chanlib.NewAdapterMux: isConnected must not be nil")
	}
	if lastInboundAt == nil {
		panic("chanlib.NewAdapterMux: lastInboundAt must not be nil")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /send", Auth(handleSend(bot, st)))
	mux.HandleFunc("POST /send-file", Auth(handleSendFile(bot, st)))
	mux.HandleFunc("POST /send-voice", Auth(handleSendVoice(bot)))
	mux.HandleFunc("POST /typing", Auth(handleTyping(bot)))
	mux.HandleFunc("POST /post", Auth(handlePost(bot)))
	mux.HandleFunc("POST /like", Auth(handleLike(bot)))
	mux.HandleFunc("POST /delete", Auth(handleDelete(bot)))
	mux.HandleFunc("POST /forward", Auth(handleForward(bot)))
	mux.HandleFunc("POST /quote", Auth(handleQuote(bot)))
	mux.HandleFunc("POST /repost", Auth(handleRepost(bot)))
	mux.HandleFunc("POST /dislike", Auth(handleDislike(bot)))
	mux.HandleFunc("POST /edit", Auth(handleEdit(bot)))
	mux.HandleFunc("POST /pin", Auth(handlePin(bot)))
	mux.HandleFunc("POST /unpin", Auth(handleUnpin(bot)))
	mux.HandleFunc("GET /health", handleHealth(name, prefixes, isConnected, lastInboundAt, st))
	// The operator dashboard (spec 6/31), routed by DAEMON name: the manifest
	// publishes /dash/teled/ while `name` is the platform, so the path is read
	// from AUTHD_SERVICE_NAME, never derived — deriving it made every adapter
	// dashboard 404 at its own URL.
	mux.HandleFunc("GET /dash/"+EnvOr("AUTHD_SERVICE_NAME", name)+"/",
		handleAdapterDash(name, prefixes, bot, isConnected, lastInboundAt, AuthdKeys()))
	if hp, ok := bot.(HistoryProvider); ok {
		mux.HandleFunc("GET /v1/history", Auth(handleHistory(hp)))
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

// jsonVerb is the one shape the simple verb handlers share: cap the body,
// decode, validate, call, write. A decode failure and a missing field answer
// with the SAME 400: the caller's fix is identical either way.
func jsonVerb[T any](msg string, valid func(T) bool, call func(T) (string, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req T
		if json.NewDecoder(r.Body).Decode(&req) != nil || !valid(req) {
			WriteErr(w, 400, msg)
			return
		}
		id, err := call(req)
		writeBotResult(w, id, err)
	}
}

// idless adapts a verb whose bot method returns only an error — the platform
// gives back no id for a reaction, a delete or an edit.
func idless[T any](f func(T) error) func(T) (string, error) {
	return func(req T) (string, error) { return "", f(req) }
}

func handleSend(bot BotHandler, st *chanstore.Store) http.HandlerFunc {
	return jsonVerb("chat_jid and content required",
		func(req SendRequest) bool { return req.ChatJID != "" && req.Content != "" },
		func(req SendRequest) (string, error) {
			return deliverOnce(st, req.IdempotencyKey, req.ChatJID, func() (string, error) {
				return bot.Send(req)
			})
		})
}

// receiveUpload parses a multipart upload into a temp dir; on any error it
// writes the HTTP response and returns ok=false. `name` is
// attacker-controlled, so filepath.Clean + Base prevents path traversal.
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
	defer func() { _ = file.Close() }()
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
		_ = os.RemoveAll(dir)
		WriteErr(w, 500, "temp file failed")
		return
	}
	_, copyErr := io.Copy(tmp, file)
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.RemoveAll(dir)
		WriteErr(w, 500, "temp file write failed")
		return
	}
	return jid, localPath, name, caption, func() { _ = os.RemoveAll(dir) }, true
}

func handleSendFile(bot BotHandler, st *chanstore.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jid, localPath, name, caption, cleanup, ok := receiveUpload(w, r, "chan-")
		if !ok {
			return
		}
		defer cleanup()
		replyTo := r.FormValue("reply_to")
		threadID := r.FormValue("thread_id")
		id, err := deliverOnce(st, r.FormValue("idempotency_key"), jid, func() (string, error) {
			return bot.SendFile(jid, localPath, name, caption, replyTo, threadID)
		})
		writeBotResult(w, id, err)
	}
}

func handleSendVoice(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		jid, localPath, _, caption, cleanup, ok := receiveUpload(w, r, "chan-voice-")
		if !ok {
			return
		}
		defer cleanup()
		threadID := r.FormValue("thread_id")
		id, err := bot.SendVoice(jid, localPath, caption, threadID)
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
	if errors.Is(err, ErrInvalidRequest) {
		WriteErr(w, 400, err.Error())
		return
	}
	WriteErr(w, 502, err.Error())
}

func handlePost(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid and content required",
		func(req PostRequest) bool { return req.ChatJID != "" && req.Content != "" },
		bot.Post)
}

func handleLike(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid and target_id required",
		func(req LikeRequest) bool { return req.ChatJID != "" && req.TargetID != "" },
		idless(bot.Like))
}

func handleDelete(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid and target_id required",
		func(req DeleteRequest) bool { return req.ChatJID != "" && req.TargetID != "" },
		idless(bot.Delete))
}

func handleForward(bot BotHandler) http.HandlerFunc {
	return jsonVerb("source_msg_id and target_jid required",
		func(req ForwardRequest) bool { return req.SourceMsgID != "" && req.TargetJID != "" },
		bot.Forward)
}

func handleQuote(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid and source_msg_id required",
		func(req QuoteRequest) bool { return req.ChatJID != "" && req.SourceMsgID != "" },
		bot.Quote)
}

func handleRepost(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid and source_msg_id required",
		func(req RepostRequest) bool { return req.ChatJID != "" && req.SourceMsgID != "" },
		bot.Repost)
}

func handleDislike(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid and target_id required",
		func(req DislikeRequest) bool { return req.ChatJID != "" && req.TargetID != "" },
		idless(bot.Dislike))
}

func handleEdit(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid, target_id and content required",
		func(req EditRequest) bool { return req.ChatJID != "" && req.TargetID != "" && req.Content != "" },
		idless(bot.Edit))
}

func handlePin(bot BotHandler) http.HandlerFunc {
	return jsonVerb("chat_jid and target_id required",
		func(req PinRequest) bool { return req.ChatJID != "" && req.TargetID != "" },
		idless(bot.Pin))
}

func handleUnpin(bot BotHandler) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, MaxAdapterJSONBody)
		var req UnpinRequest
		if json.NewDecoder(r.Body).Decode(&req) != nil || req.ChatJID == "" {
			WriteErr(w, 400, "chat_jid required")
			return
		}
		if !req.All && req.TargetID == "" {
			WriteErr(w, 400, "target_id required (or set all=true)")
			return
		}
		writeBotResult(w, "", bot.Unpin(req))
	}
}

// staleThresholds: /health flips to stale when lastInboundAt is older than this.
// email: IDLE+poll-fallback is lumpier. reddit: sparse subreddits can be quiet for hours.
var staleThresholds = map[string]time.Duration{
	"email":  10 * time.Minute,
	"reddit": 60 * time.Minute,
}

// Stale inbound is informational only: a quiet-but-connected channel is
// normal, so /health stays 200 with status=stale. Platform-link death is
// isConnected() (503), which each adapter drives from its own liveness probe
// — slakd via auth.test, not silence; a quiet workspace must not 503.
const defaultStaleThreshold = 5 * time.Minute

func handleHealth(name string, prefixes []string, isConnected func() bool, lastInboundAt func() int64, st *chanstore.Store) http.HandlerFunc {
	threshold := defaultStaleThreshold
	if t, ok := staleThresholds[name]; ok {
		threshold = t
	}
	return func(w http.ResponseWriter, r *http.Request) {
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
		// Inbound routd has not accepted yet (BUGS L25). Reported, not fatal:
		// the sweep is working through it and the adapter is otherwise healthy.
		if n, err := PendingInboundCount(r.Context(), st); err != nil {
			slog.Error("health: pending inbound count failed", "err", err)
		} else if n > 0 {
			resp["pending_inbound"] = n
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

// FileProxyOpts wires a /files/<id> handler that resolves an opaque id to a
// CDN URL and streams it back through ProxyFile. Resolve takes a context and
// may fail, because the mapping lives in the adapter's store (BUGS L163):
// "the store would not answer" is not "no such file", and a 404 for it would
// tell routd's enrich the sender attached nothing.
type FileProxyOpts struct {
	Resolve  func(ctx context.Context, id string) (string, bool, error)
	Validate func(url string) bool // optional: reject a resolved URL before the fetch
	Decorate func(*http.Request)   // optional: tweak the upstream request
	Client   *http.Client          // optional; defaults to a 30s-timeout client
	MaxBytes int64                 // proxy cap; 0 ⇒ 20 MiB default in ProxyFile
}

var defaultProxyClient = &http.Client{Timeout: 30 * time.Second}

// FileProxyHandler returns a handler that strips /files/ from the URL path,
// resolves the id, fetches the upstream URL, and streams it back. 400 on
// empty id, 404 when Resolve misses, 502 on upstream failure.
func FileProxyHandler(opts FileProxyOpts) http.HandlerFunc {
	if opts.Resolve == nil {
		panic("chanlib.FileProxyHandler: Resolve must not be nil")
	}
	client := opts.Client
	if client == nil {
		client = defaultProxyClient
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/files/")
		if id == "" {
			WriteErr(w, 400, "file id required")
			return
		}
		cdnURL, ok, err := opts.Resolve(r.Context(), id)
		if err != nil {
			slog.Error("file proxy: resolve failed", "id", id, "err", err)
			WriteErr(w, 502, "file lookup failed")
			return
		}
		if !ok {
			WriteErr(w, 404, "not found")
			return
		}
		if opts.Validate != nil && !opts.Validate(cdnURL) {
			WriteErr(w, 400, "disallowed url")
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), "GET", cdnURL, nil)
		if err != nil {
			WriteErr(w, 502, "cdn fetch failed")
			return
		}
		req.Header.Set("User-Agent", UserAgent)
		if opts.Decorate != nil {
			opts.Decorate(req)
		}
		resp, err := client.Do(req)
		if err != nil {
			WriteErr(w, 502, "cdn fetch failed")
			return
		}
		defer func() { _ = resp.Body.Close() }()
		ProxyFile(w, resp, opts.MaxBytes)
	}
}
