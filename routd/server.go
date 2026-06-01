package routd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
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
	// Extended verbs — the social/feed surface ported from gated's egress.
	// Post authors a fresh top-level post; Forward/Quote/Repost relay or
	// amplify an existing message; Dislike is the native-downvote reaction;
	// SetSuggestions/SetName stage Slack assistant-pane controls.
	Post(jid, content string, mediaPaths []string) (platformID string, err error)
	Forward(sourceMsgID, targetJID, comment string) (platformID string, err error)
	Quote(jid, sourceMsgID, comment string) (platformID string, err error)
	Repost(jid, sourceMsgID string) (platformID string, err error)
	Dislike(jid, platformID string) error
	SetSuggestions(jid string, prompts []core.PanePrompt) error
	SetName(jid, title string) error
	// RoundDone notifies the web SSE channel that a turn closed, so a /chat
	// client stops waiting. folder is the web-chat folder (web: prefix stripped).
	RoundDone(folder, turnID, status, errMsg string) error
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

	// groupsDir/webDir back the file-path agent tools (send_file, vhosts) the
	// in-process MCP socket exposes; set via SetDirs from the cfg dirs.
	groupsDir string
	webDir    string

	// Channel-registration surface (ported from gated's api). reg==nil leaves
	// the /v1/channels endpoints unmounted (pure REST tests). on{Register,
	// Deregister} mirror gated's live-channel hooks so the Deliverer reuses a
	// per-adapter HTTPChannel and its retry outbox.
	reg          *chanreg.Registry
	onRegister   func(name string, ch *chanreg.HTTPChannel)
	onDeregister func(name string)
}

// NewServer wires the HTTP server. loop may be nil for pure REST tests.
func NewServer(db *DB, loop *Loop, deliver Deliverer, verify Verifier, engagementTTL time.Duration, webHost string) *Server {
	if engagementTTL == 0 {
		engagementTTL = 30 * time.Minute
	}
	return &Server{db: db, loop: loop, deliver: deliver, verify: verify, engagementT: engagementTTL, webHost: webHost}
}

// SetDirs supplies the group + web roots the in-process MCP file-path tools
// (send_file, vhosts) resolve against. Set post-construction in main wiring.
func (s *Server) SetDirs(groupsDir, webDir string) {
	s.groupsDir = groupsDir
	s.webDir = webDir
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
	// REST read/manage surface — the twin of routd's in-process MCP StoreFns
	// (the agent reaches the same data over the socket, humans/tools over HTTP)
	mux.HandleFunc("GET /v1/messages/inspect", s.handleInspectMessages)
	mux.HandleFunc("GET /v1/messages/thread", s.handleThreadMessages)
	mux.HandleFunc("GET /v1/messages/find", s.handleFindMessages)
	mux.HandleFunc("GET /v1/routing/resolve", s.handleRoutingResolve)
	mux.HandleFunc("GET /v1/routing/errored", s.handleErroredChats)
	mux.HandleFunc("GET /v1/engagement", s.handleEngagementGet)
	mux.HandleFunc("POST /v1/engagement", s.handleEngagementSet)
	mux.HandleFunc("GET /v1/sessions", s.handleSessionGet)
	mux.HandleFunc("POST /v1/cost", s.handleCost)
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
	s.mountChannels(mux)
	return mux
}

// authz verifies the bearer token and, when scopes are required, checks the
// token carries one of them (any-of). Returns the token's sub + arz/folder
// claim. verify==nil is open (single-tenant / local-dev): ok=true, empty
// sub/folder. Fails CLOSED — a verify error or a missing scope is denied.
// Service subs (service:<daemon>) carry the daemon's broad scopes like any
// other token; there is no implicit bypass.
func (s *Server) authz(w http.ResponseWriter, r *http.Request, anyScope ...string) (sub, folder string, ok bool) {
	if s.verify == nil {
		return "", "", true // tests / local-dev without a verifier
	}
	sub, scope, folder, err := s.verify.Verify(r)
	if err != nil {
		writeErr(w, 401, "unauthorized", err.Error())
		return "", "", false
	}
	if len(anyScope) > 0 && !hasAnyScope(scope, anyScope) {
		writeErr(w, 403, "forbidden", "missing scope "+strings.Join(anyScope, " or "))
		return "", "", false
	}
	return sub, folder, true
}

// hasAnyScope reports whether held grants any of the wanted "resource:verb"
// scopes. A held "resource:*" covers any verb on that resource (auth.HasScope);
// an exact string also matches (covers the "resource:verb:own_group" form the
// spec uses for folder-bound agent scopes, where the folder claim is the bound).
func hasAnyScope(held, wanted []string) bool {
	for _, w := range wanted {
		if i := strings.IndexByte(w, ':'); i > 0 {
			if auth.HasScope(held, w[:i], w[i+1:]) {
				return true
			}
		}
		for _, h := range held {
			if h == w {
				return true
			}
		}
	}
	return false
}

// ownsFolder reports whether the token's folder claim owns target (equal or
// ancestor). An empty token folder (open mode) owns everything; an empty
// target is owned by anyone (no folder-bound resource). Fails CLOSED for a
// scoped token acting outside its subtree.
func ownsFolder(tokenFolder, target string) bool {
	if tokenFolder == "" || target == "" {
		return true
	}
	return descendant(target, tokenFolder)
}

func (s *Server) authed(w http.ResponseWriter, r *http.Request, anyScope ...string) bool {
	_, _, ok := s.authz(w, r, anyScope...)
	return ok
}

// adapterName extracts the calling adapter's name from its service token
// --- ingress ---

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	sub, _, ok := s.authz(w, r, "messages:write")
	if !ok {
		return
	}
	// adapter name for the minted id is the verified caller (service:<adapter>
	// → <adapter>); "adapter" when unverified (local-dev). Reuses the authz
	// Verify above instead of a second Verify call.
	adapter := strings.TrimPrefix(sub, "service:")
	if adapter == "" {
		adapter = "adapter"
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
	// Idempotency for the append-only log keys on the message id (the PK).
	// X-Idempotency-Key is honored ONLY when id is absent: routd mints
	// id=<adapter>-<key> so the two keys collapse. A stable id AND a key
	// together is ambiguous (spec 5/E § POST /v1/messages key rules).
	idemKey := r.Header.Get("X-Idempotency-Key")
	switch {
	case m.ID != "" && idemKey != "":
		writeErr(w, 400, "ambiguous_idempotency", "send either a stable id or X-Idempotency-Key, not both")
		return
	case m.ID == "" && idemKey != "":
		m.ID = adapter + "-" + idemKey
	case m.ID == "":
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
	// Reaction topic-inheritance: a reaction/reply with no topic of its own
	// inherits the parent message's topic so it routes to the parent's
	// thread, not the main topic (spec 5/E § Channel ingress).
	if m.Topic == "" && m.ReplyTo != "" {
		m.Topic = s.db.TopicByID(m.ReplyTo)
	}
	row := buildMessageRow(m, ts, verb)
	// Engagement is NOT committed at ingress: the owning folder isn't known
	// until route resolution. routd defers the engagement claim to dispatch
	// time (appendAndDeliver bumps it with the resolved folder), mirroring
	// gated's makeOutputCallback/poll bump sites. A pre-PutMessage claim with
	// an empty folder would make Engaged return ("", true) and misroute.
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
	if !s.authed(w, r, "messages:write") {
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
