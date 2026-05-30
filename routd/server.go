package routd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// Deliverer fans an outbound message out to the owning platform adapter
// (the adapter's POST /v1/send). routd writes the messages row first
// (append-then-deliver); the Deliverer is the egress half. Production
// resolves the adapter by jid prefix; tests inject a fake.
type Deliverer interface {
	// Send delivers text to jid threaded under replyToID/threadID, using
	// idempotencyKey (the bot row's stable message_id) so the adapter
	// dedups platform-side. Returns the platform-native id.
	Send(jid, text, replyToID, threadID, idempotencyKey string) (platformID string, err error)
	// React/Edit/Delete/Pin/Unpin mutate an existing platform message.
	React(jid, platformID, reaction string) error
	Edit(jid, platformID, content string) error
	Delete(jid, platformID string) error
	Pin(jid, platformID string) error
	Unpin(jid, platformID string, all bool) error
	// Document delivers a file at path. The file lives on the shared group
	// volume both routd and the adapter mount.
	Document(jid, path, name, caption, replyToID, idempotencyKey string) (platformID string, err error)
}

// Verifier offline-verifies inbound bearer tokens (agent capability /
// adapter service tokens) against authd's keys. routd is a verifier, not a
// signer (spec 5/E § Auth).
type Verifier interface {
	Verify(r *http.Request) (sub string, scope []string, folder string, err error)
}

// Server is routd's HTTP face: ingress + the /v1/turns callback surface +
// route/web-route/route-token CRUD + outbound passthrough. It holds the
// runed client to dispatch runs.
type Server struct {
	db          *DB
	loop        *Loop
	deliver     Deliverer
	verify      Verifier
	engagementT time.Duration
	webHost     string
}

// NewServer wires the HTTP server. loop may be nil for pure REST tests.
func NewServer(db *DB, loop *Loop, deliver Deliverer, verify Verifier, engagementTTL time.Duration, webHost string) *Server {
	if engagementTTL == 0 {
		engagementTTL = 30 * time.Minute
	}
	return &Server{db: db, loop: loop, deliver: deliver, verify: verify, engagementT: engagementTTL, webHost: webHost}
}

// Handler builds the routed mux. GET /health and /openapi.json are public;
// everything else is bearer-gated by the Verifier.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/outbound", s.handleOutbound)
	mux.HandleFunc("GET /v1/routes", s.handleRoutesList)
	mux.HandleFunc("GET /v1/routes/{id}", s.handleRouteGet)
	mux.HandleFunc("PUT /v1/routes", s.handleRoutesReplace)
	mux.HandleFunc("POST /v1/routes", s.handleRouteAdd)
	mux.HandleFunc("DELETE /v1/routes/{id}", s.handleRouteDelete)
	mux.HandleFunc("GET /v1/web_routes", s.handleWebRoutesList)
	mux.HandleFunc("PUT /v1/web_routes", s.handleWebRoutePut)
	mux.HandleFunc("DELETE /v1/web_routes", s.handleWebRouteDelete)
	mux.HandleFunc("POST /v1/route_tokens/chat", s.handleTokenChat)
	mux.HandleFunc("POST /v1/route_tokens/hook", s.handleTokenHook)
	mux.HandleFunc("GET /v1/route_tokens", s.handleTokenList)
	mux.HandleFunc("DELETE /v1/route_tokens/{jid}", s.handleTokenRevoke)
	mux.HandleFunc("POST /v1/route_tokens/resolve", s.handleTokenResolve)
	// turn callbacks (the sole-appender surface)
	mux.HandleFunc("POST /v1/turns/{turn_id}/reply", s.handleReply)
	mux.HandleFunc("POST /v1/turns/{turn_id}/send", s.handleSend)
	mux.HandleFunc("POST /v1/turns/{turn_id}/document", s.handleDocument)
	mux.HandleFunc("GET /v1/turns/{turn_id}/history", s.handleHistory)
	mux.HandleFunc("POST /v1/turns/{turn_id}/like", s.handleLike)
	mux.HandleFunc("POST /v1/turns/{turn_id}/edit", s.handleEdit)
	mux.HandleFunc("POST /v1/turns/{turn_id}/delete", s.handleDelete)
	mux.HandleFunc("POST /v1/turns/{turn_id}/pin", s.handlePin)
	mux.HandleFunc("POST /v1/turns/{turn_id}/unpin", s.handleUnpin)
	mux.HandleFunc("POST /v1/turns/{turn_id}/result", s.handleResult)
	return mux
}

func (s *Server) authed(w http.ResponseWriter, r *http.Request) bool {
	if s.verify == nil {
		return true // tests without a verifier
	}
	if _, _, _, err := s.verify.Verify(r); err != nil {
		writeErr(w, 401, "unauthorized", err.Error())
		return false
	}
	return true
}

// --- ingress ---

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	var m apiv1.Message
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if m.ChatJID == "" || m.Content == "" {
		writeErr(w, 400, "missing_field", "chat_jid and content required")
		return
	}
	if m.ID == "" {
		m.ID = "in-" + randHex(8)
	}
	ts := time.Now().UTC()
	if m.Timestamp > 0 {
		ts = time.Unix(m.Timestamp, 0).UTC()
	}
	// 5/L reply-to-bot → mention promotion: an inbound replying to a bot
	// row is promoted to verb=mention so routing sees a uniform trigger.
	verb := m.Verb
	if verb == "" {
		verb = "message"
	}
	if m.ReplyTo != "" && s.replyTargetIsBot(m.ReplyTo) {
		verb = "mention"
	}
	row := buildMessageRow(m, ts, verb)
	// engagement-on-mention commits with the row (before PutMessage).
	if verb == "mention" {
		_ = s.db.SetEngagement(m.ChatJID, m.Topic, "", s.engagementT)
	}
	if err := s.db.PutMessage(row); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	_ = s.db.SetChatIsGroup(m.ChatJID, m.IsGroup)
	if s.loop != nil {
		s.loop.Enqueue(m.ChatJID)
	}
	writeJSON(w, 200, apiv1.MessageAck{OK: true, ID: m.ID})
}

func (s *Server) replyTargetIsBot(id string) bool {
	var bot int
	s.db.db.QueryRow("SELECT is_bot_message FROM messages WHERE id=? OR platform_id=?", id, id).Scan(&bot)
	return bot == 1
}

func (s *Server) handleOutbound(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	var req apiv1.OutboundRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if s.deliver == nil {
		writeErr(w, 502, "no_channel", "no deliverer configured")
		return
	}
	if _, err := s.deliver.Send(req.JID, req.Text, "", "", ""); err != nil {
		writeErr(w, 502, "delivery_failed", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// --- helpers ---

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, apiv1.Err{Error: code, Message: msg})
}

func atoi64(s string) (int64, bool) {
	n, err := strconv.ParseInt(s, 10, 64)
	return n, err == nil
}

func trimWeb(jid string) string {
	if strings.HasPrefix(jid, "web:") {
		return strings.TrimPrefix(jid, "web:")
	}
	return jid
}

var _ = slog.Default
