package routd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
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
	// Typing toggles the platform "typing…" presence while the agent works the
	// turn (best-effort; web / no-presence channels are no-ops).
	Typing(jid string, on bool) error
	// Document delivers a file at path. The file lives on the shared group
	// volume both routd and the adapter mount.
	Document(jid, path, name, caption, replyToID, idempotencyKey string) (platformID string, err error)
	// SendVoice delivers a synthesized audio file (Opus/Ogg) as a voice note,
	// threaded under threadID. routd synthesizes via TTS first (tts.go), then
	// hands the cached audio path here for the owning adapter to upload.
	SendVoice(jid, audioPath, caption, threadID string) (platformID string, err error)
	// Extended verbs — the social/feed surface. Post authors a fresh top-level
	// post; Forward/Quote/Repost relay or
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
	// FetchHistory proxies to the owning adapter's GET /v1/history (the
	// fetch_history MCP tool's platform-truth source). Returns the raw JSON
	// HistoryResponse bytes. (chanreg.ErrNoChannel, nil) when no adapter owns
	// the jid or it lacks the cap → the caller falls back to the local DB.
	FetchHistory(jid string, before time.Time, limit int) ([]byte, error)
}

// Verifier offline-verifies inbound bearer tokens (agent capability / adapter
// service tokens) against authd's keys. routd is a verifier, not a signer.
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

	// hostingDomain + vhostAliases mirror proxyd's vhost config so the
	// get_web_presence MCP tool + /v1/web_presence REST twin can report a
	// folder's derived/aliased canonical host (spec 5/V). Set via SetVhosts.
	hostingDomain string
	vhostAliases  map[string]string

	// identity resolves a sender sub → canonical identity via authd's
	// GET /v1/identities/{sub}. Backs the inspect_identity MCP tool; nil → the
	// tool answers unclaimed. Set via SetIdentityResolver.
	identity IdentityResolver

	// sessions federates the session_log run history via runed's
	// GET /v1/sessions/recent. Backs the inspect_session MCP tool's recent rows;
	// nil → no prior sessions. Set via SetSessionResolver.
	sessions SessionResolver

	// disabledGroups is SEND_DISABLED_GROUPS: muted folders whose outbound is
	// persisted (the row lands status=sent) but NOT delivered to the platform.
	// SEND_DISABLED_CHANNELS (jid-prefix mute) stays in the Deliverer; this is the
	// group-folder mute. Set via SetDisabledGroups.
	disabledGroups []string

	// groupsDir/webDir back the file-path agent tools (send_file, vhosts) the
	// in-process MCP socket exposes; set via SetDirs from the cfg dirs.
	groupsDir string
	webDir    string

	// tts carries the send_voice synthesis config (TTS_* env). Zero-value
	// (Enabled=false) leaves the send_voice MCP tool returning the
	// not-configured/unsupported error. Set via SetTTS from the cmd layer.
	tts ttsConfig

	// audit receives system events for mutating MCP tool calls. It appends to
	// routd's own audit-system.jl in DATA_DIR — observability only. nil → slog-only
	// (the audit.noop value EmitSystem treats as disabled). Set via SetAudit.
	audit *audit.Audit

	// connectors is the discovered MCP-connector tool catalog (connectors.toml),
	// loaded once at boot and registered through every per-turn MCP socket.
	// nil/empty leaves the connector path off. Set via SetConnectors.
	connectors []ipc.ConnectorTool

	// Channel-registration surface. reg==nil leaves the /v1/channels endpoints
	// unmounted (pure REST tests). on{Register,Deregister} keep the Deliverer's
	// per-adapter HTTPChannel (and its retry outbox) in sync.
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

// SetVhosts supplies HOSTING_DOMAIN + the WEB_VHOST_ALIASES host→folder map so
// get_web_presence can report a folder's derived/aliased canonical host. These
// mirror proxyd's vhost config; routd never serves vhosts, it only reports them.
func (s *Server) SetVhosts(hostingDomain string, aliases map[string]string) {
	s.hostingDomain = hostingDomain
	s.vhostAliases = aliases
}

// webPresence is the single renderer feeding both the get_web_presence MCP tool
// (via buildGatedFns) and the /v1/web_presence REST twin.
func (s *Server) webPresence(folder string) ipc.WebPresence {
	return ipc.WebPresenceFor(folder, s.webHost, s.hostingDomain, s.vhostAliases)
}

// SetDisabledGroups supplies SEND_DISABLED_GROUPS: muted folders whose outbound
// persists but is not delivered. Set post-construction.
func (s *Server) SetDisabledGroups(folders []string) { s.disabledGroups = folders }

// mutedGroup reports whether folder is in SEND_DISABLED_GROUPS — outbound for it
// persists (the row lands status=sent) but is never delivered to the platform.
func (s *Server) mutedGroup(folder string) bool {
	for _, f := range s.disabledGroups {
		if strings.EqualFold(f, folder) {
			return true
		}
	}
	return false
}

// SetTTS supplies the send_voice synthesis config. Set post-construction in
// main wiring; the zero value leaves voice off.
func (s *Server) SetTTS(c ttsConfig) { s.tts = c }

// SetAudit injects the audit writer so mutating MCP tool calls emit
// audit-system.jl events. routd's audit is observability (slog + .jl webhook
// stream).
func (s *Server) SetAudit(a *audit.Audit) { s.audit = a }

// SetConnectors supplies the discovered connector-tool catalog (LoadConnectors).
// Every per-turn MCP socket registers it; nil/empty leaves the path off.
func (s *Server) SetConnectors(c []ipc.ConnectorTool) { s.connectors = c }

// SetIdentityResolver wires the authd identity client backing inspect_identity.
// nil → the tool answers unclaimed. Set post-construction in main wiring.
func (s *Server) SetIdentityResolver(r IdentityResolver) { s.identity = r }

// resolveIdentity is the StoreFns.GetIdentityForSub backing: it delegates to the
// authd resolver. A nil resolver returns the unclaimed shape.
func (s *Server) resolveIdentity(sub string) (ipc.Identity, []string, bool) {
	if s.identity == nil {
		return ipc.Identity{}, nil, false
	}
	return s.identity.Resolve(sub)
}

// SetSessionResolver wires the runed session client backing inspect_session.
// nil → no prior sessions. Set post-construction in main wiring.
func (s *Server) SetSessionResolver(r SessionResolver) { s.sessions = r }

// recentSessions is the StoreFns.RecentSessions backing: it federates to runed's
// GET /v1/sessions/recent. A nil resolver returns nil.
func (s *Server) recentSessions(folder string, n int) []core.SessionRecord {
	if s.sessions == nil {
		return nil
	}
	return s.sessions.RecentSessions(folder, n)
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
	mux.HandleFunc("GET /v1/web_presence", s.handleWebPresence)
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
	mux.HandleFunc("GET /v1/users/{sub}/scopes", s.handleUserScopes)
	mux.HandleFunc("POST /v1/acl", s.handleACLAdd)
	mux.HandleFunc("DELETE /v1/acl", s.handleACLRemove)
	mux.HandleFunc("POST /v1/secrets", s.handleSecretSet)
	mux.HandleFunc("DELETE /v1/secrets/{key}", s.handleSecretDelete)
	mux.HandleFunc("POST /v1/pane", s.handlePaneSet)
	mux.HandleFunc("GET /v1/tasks/due", s.handleTasksDue)
	mux.HandleFunc("POST /v1/tasks/runlog", s.handleTaskRunLog)
	mux.HandleFunc("POST /v1/tasks/{id}/reschedule", s.handleTaskReschedule)
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
	// social/feed turn-face — the REST twins of the post/forward/quote/repost/
	// send_voice MCP tools; pure relays to the same Deliverer methods (5/5).
	mux.HandleFunc("POST /v1/turns/{turn_id}/post", s.handlePost)
	mux.HandleFunc("POST /v1/turns/{turn_id}/forward", s.handleForward)
	mux.HandleFunc("POST /v1/turns/{turn_id}/quote", s.handleQuote)
	mux.HandleFunc("POST /v1/turns/{turn_id}/repost", s.handleRepost)
	mux.HandleFunc("POST /v1/turns/{turn_id}/send_voice", s.handleSendVoice)
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

// ownsJID reports whether a token scoped to tokenFolder may act on jid's chat.
// It mirrors ipc.authorizeJID's resolution EXACTLY so the REST and MCP faces
// gate identically (CLAUDE.md "auth is a uniform middleware"): resolve the jid's
// routing target, fall back to the web:<folder> 1:1 binding when no route row,
// allow when that target is in the token's subtree OR (verb-agnostic) the token's
// folder is itself a route target for jid (mention-only subfolder). Empty token
// folder (root / service token) owns everything.
func (s *Server) ownsJID(tokenFolder, jid string) bool {
	if tokenFolder == "" {
		return true
	}
	target := s.db.DefaultFolderForJID(jid)
	if target == "" {
		if folder, ok := strings.CutPrefix(jid, "web:"); ok {
			target = folder
		}
	}
	if target != "" && ownsFolder(tokenFolder, target) {
		return true
	}
	return s.db.JIDRoutableToFolder(jid, tokenFolder)
}

// denyCrossFolder writes the 403 a scoped token gets when it targets a chat/
// folder outside its subtree — the REST twin of ipc.authorizeJID's error.
func denyCrossFolder(w http.ResponseWriter, jid string) {
	writeErr(w, 403, "forbidden", "chat "+jid+" is outside your folder")
}

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
	// Ingress JID-ownership (ported from gated api.handleMessage's entry.Owns
	// reject): a platform-scheme inbound must land under a registered channel's
	// prefixes — a caller can't inject JIDs no adapter owns. Internal schemes
	// (web:/hook:/bare-folder, used by web chat + timed + onbod) carry no channel
	// prefix and are exempt. NOTE the split decouples the JWT principal
	// (AUTHD_SERVICE_NAME, e.g. teled) from the channel-registration name (e.g.
	// telegram), so this binds to "some registered channel owns it", not to the
	// caller's own entry; cross-adapter spoofing needs a principal↔channel map
	// that the registry doesn't carry yet (see report).
	if s.reg != nil && isChannelJID(m.ChatJID) && s.reg.ForJID(m.ChatJID) == nil {
		writeErr(w, 400, "jid_prefix_mismatch", "no registered channel owns "+m.ChatJID)
		return
	}
	// Idempotency for the append-only log keys on the message id (the PK).
	// X-Idempotency-Key is honored ONLY when id is absent: routd mints
	// id=<adapter>-<key> so the two keys collapse. A stable id AND a key together
	// is ambiguous.
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
	// reply-to-bot → mention promotion: an inbound replying to a bot row is
	// promoted to verb=mention so routing sees a uniform trigger.
	verb := m.Verb
	if verb == "" {
		verb = "message"
	}
	// An untrusted sender (emaid marks unverified senders verb=untrusted, spec
	// 10/17) must NOT escalate to a mention by replying to a bot message — that
	// would let a spoofed inbound drive an agent turn. gated guarded this; the
	// split had dropped it.
	if verb != "untrusted" && m.ReplyTo != "" && s.replyTargetIsBot(m.ReplyTo) {
		verb = "mention"
	}
	// Reaction topic-inheritance: a reaction/reply with no topic of its own
	// inherits the parent message's topic so it routes to the parent's thread, not
	// the main topic.
	if m.Topic == "" && m.ReplyTo != "" {
		m.Topic = s.db.TopicByID(m.ReplyTo)
	}
	row := buildMessageRow(m, ts, verb)
	// Engagement is NOT committed at ingress: the owning folder isn't known until
	// route resolution. routd defers the engagement claim to dispatch time
	// (appendAndDeliver bumps it with the resolved folder). A pre-PutMessage claim
	// with an empty folder would make Engaged return ("", true) and misroute.
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

// handleUserScopes serves the login-time scope snapshot authd pulls at session
// issuance. It evaluates {sub}'s scopes against routd's acl rows
// (membership-expanded). 200 {"scope":[...],"folder":"..."} when the sub holds
// grants; 404 {"error":"no_grants"} when it holds none. Bearer-gated by
// grants:read.
func (s *Server) handleUserScopes(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "grants:read") {
		return
	}
	sub := r.PathValue("sub")
	scope := s.db.UserScopes(sub)
	if len(scope) == 0 {
		writeErr(w, 404, "no_grants", "no grants for sub "+sub)
		return
	}
	// folder is the single-subtree bound for the minted token: the lone scope
	// when the sub holds exactly one, else empty (no single bound to claim).
	folder := ""
	if len(scope) == 1 {
		folder = scope[0]
	}
	writeJSON(w, 200, map[string]any{"scope": scope, "folder": folder})
}

// aclWriteBody is the POST/DELETE /v1/acl payload: grant or revoke one
// principal's access to a scope. The operator `**` pattern maps to role:operator
// membership, so one principal, one scope covers both per-folder admin rows AND
// the operator role. action/effect default to admin/allow (the grant shape); set
// them for a non-default rule.
type aclWriteBody struct {
	Principal string `json:"principal"`
	Scope     string `json:"scope"`
	Action    string `json:"action"`
	Effect    string `json:"effect"`
	GrantedBy string `json:"granted_by"`
}

// grantACL is the single ACL-grant writer behind both REST (handleACLAdd) and
// MCP (add_acl). Defaults action/effect/grantedBy; scope "**" → operator-role
// membership, else one acl row. One renderer, many sinks (CLAUDE.md).
func (s *Server) grantACL(principal, scope, action, effect, grantedBy string) error {
	if grantedBy == "" {
		grantedBy = "routd"
	}
	if scope == "**" {
		return s.db.AddMembership(principal, "role:operator", grantedBy)
	}
	if action == "" {
		action = "admin"
	}
	if effect == "" {
		effect = "allow"
	}
	return s.db.AddACLRow(core.ACLRow{
		Principal: principal, Action: action, Scope: scope,
		Effect: effect, GrantedBy: grantedBy,
	})
}

// revokeACL is the single ACL-revoke writer behind both REST (handleACLRemove)
// and MCP (remove_acl). Mirrors grantACL.
func (s *Server) revokeACL(principal, scope, action, effect, grantedBy string) error {
	if scope == "**" {
		return s.db.RemoveMembership(principal, "role:operator")
	}
	if action == "" {
		action = "admin"
	}
	if effect == "" {
		effect = "allow"
	}
	return s.db.RemoveACLRow(core.ACLRow{
		Principal: principal, Action: action, Scope: scope, Effect: effect,
	})
}

// handleACLAdd grants one acl row (or operator membership for scope=="**").
// Bearer-gated by acl:write. Shares grantACL with the MCP add_acl tool.
func (s *Server) handleACLAdd(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "acl:write") {
		return
	}
	var body aclWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if body.Principal == "" || body.Scope == "" {
		writeErr(w, 400, "missing_field", "principal and scope required")
		return
	}
	if err := s.grantACL(body.Principal, body.Scope, body.Action, body.Effect, body.GrantedBy); err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// handleACLRemove revokes one acl row (or operator membership for scope=="**").
// Bearer-gated by acl:write. Shares revokeACL with the MCP remove_acl tool.
func (s *Server) handleACLRemove(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "acl:write") {
		return
	}
	var body aclWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if body.Principal == "" || body.Scope == "" {
		writeErr(w, 400, "missing_field", "principal and scope required")
		return
	}
	if err := s.revokeACL(body.Principal, body.Scope, body.Action, body.Effect, body.GrantedBy); err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// secretWriteBody is the POST /v1/secrets payload: the operator sets one
// folder- or user-scoped secret. scope is "folder" or "user"; scope_id is the
// folder path or user sub; key is an ENV-style name; value is the plaintext
// (sealed at rest under SECRETS_KEY before it lands in routd.db).
type secretWriteBody struct {
	Scope   string `json:"scope"`
	ScopeID string `json:"scope_id"`
	Key     string `json:"key"`
	Value   string `json:"value"`
}

// handleSecretSet seals + upserts one secret (the operator write path).
// Bearer-gated by secrets:write. Validates scope kind + scope_id + key non-empty;
// the at-rest encoding is v2: sealed when a keyring is set, so connector injection
// reads it back through FolderSecrets.
func (s *Server) handleSecretSet(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "secrets:write") {
		return
	}
	var body secretWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if body.Value == "" {
		writeErr(w, 400, "missing_field", "value required")
		return
	}
	if err := s.db.SetSecret(store.SecretScope(body.Scope), body.ScopeID, body.Key, body.Value); err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// handleSecretDelete removes one secret. Bearer-gated by secrets:write. The scope
// + scope_id come from the query (?scope=&scope_id=); the key is the path segment.
// 404 when no row matched.
func (s *Server) handleSecretDelete(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "secrets:write") {
		return
	}
	scope := r.URL.Query().Get("scope")
	scopeID := r.URL.Query().Get("scope_id")
	key := r.PathValue("key")
	err := s.db.DeleteSecret(store.SecretScope(scope), scopeID, key)
	if errors.Is(err, store.ErrSecretNotFound) {
		writeErr(w, 404, "not_found", "no such secret")
		return
	}
	if err != nil {
		writeErr(w, 400, "invalid", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// paneSetBody is the POST /v1/pane payload: slakd's three Slack-pane writes. op
// selects the write:
//   - "open": UpsertPane(team,user,thread,channel) — creates/refreshes the row.
//   - "context": SetPaneContext(team,user,thread, jid) — updates the workspace
//     channel the user is viewing (empty jid clears it).
//   - "" (default): SetPaneContextByChannel(channel, jid) — the by-channel
//     context update.
type paneSetBody struct {
	Op        string `json:"op"`
	TeamID    string `json:"team_id"`
	UserID    string `json:"user_id"`
	ThreadTS  string `json:"thread_ts"`
	ChannelID string `json:"channel_id"`
	JID       string `json:"jid"`
}

// handlePaneSet performs slakd's pane write. Bearer-gated by messages:write (the
// scope slakd's adapter token carries).
func (s *Server) handlePaneSet(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "messages:write") {
		return
	}
	var body paneSetBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	var err error
	switch body.Op {
	case "open":
		if body.TeamID == "" || body.UserID == "" || body.ThreadTS == "" || body.ChannelID == "" {
			writeErr(w, 400, "missing_field", "team_id, user_id, thread_ts, channel_id required for open")
			return
		}
		err = s.db.UpsertPane(body.TeamID, body.UserID, body.ThreadTS, body.ChannelID)
	case "context":
		if body.TeamID == "" || body.UserID == "" || body.ThreadTS == "" {
			writeErr(w, 400, "missing_field", "team_id, user_id, thread_ts required for context")
			return
		}
		err = s.db.SetPaneContextByTriple(body.TeamID, body.UserID, body.ThreadTS, body.JID)
	default:
		if body.ChannelID == "" {
			writeErr(w, 400, "missing_field", "channel_id required")
			return
		}
		err = s.db.SetPaneContext(body.ChannelID, body.JID)
	}
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// dueTask is the GET /v1/tasks/due row: exactly the fields timed's fire loop
// needs to enqueue a message + reschedule.
type dueTask struct {
	ID          string `json:"id"`
	ChatJID     string `json:"chat_jid"`
	Prompt      string `json:"prompt"`
	Cron        string `json:"cron"`
	ContextMode string `json:"context_mode"`
}

// handleTasksDue atomically claims (marks 'firing') and returns the tasks whose
// next_run has passed — the read half of timed's fire loop. Bearer-gated by
// tasks:read.
func (s *Server) handleTasksDue(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:read") {
		return
	}
	tasks, err := s.db.DueTasks(time.Now())
	if err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	out := make([]dueTask, len(tasks))
	for i, t := range tasks {
		out[i] = dueTask{ID: t.ID, ChatJID: t.ChatJID, Prompt: t.Prompt,
			Cron: t.Cron, ContextMode: t.ContextMode}
	}
	writeJSON(w, 200, map[string]any{"tasks": out})
}

// taskRunLogBody is the POST /v1/tasks/runlog payload: timed records one task
// run (success or error) after firing. duration_ms is the fire latency.
type taskRunLogBody struct {
	TaskID     string `json:"task_id"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

// handleTaskRunLog appends one task_run_logs row — the write half of timed's
// fire loop. Bearer-gated by tasks:write.
func (s *Server) handleTaskRunLog(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:write") {
		return
	}
	var body taskRunLogBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if body.TaskID == "" || body.Status == "" {
		writeErr(w, 400, "missing_field", "task_id and status required")
		return
	}
	if err := s.db.RecordTaskRun(store.TaskRunLog{
		TaskID: body.TaskID, Status: body.Status, Error: body.Error, DurationMS: body.DurationMS,
	}); err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// rescheduleBody is the POST /v1/tasks/{id}/reschedule payload: timed sends the
// next_run it computed client-side + the target status (active for recurring,
// completed for one-shot). An empty next_run clears it (one-shot completion).
type rescheduleBody struct {
	NextRun string `json:"next_run"`
	Status  string `json:"status"`
}

// handleTaskReschedule sets a fired task's next_run + status — the reschedule
// half of timed's fire loop. Bearer-gated by tasks:write. timed computes next_run
// (cron/interval) and passes status so routd stays a boring writer: recurring →
// active, one-shot → completed.
func (s *Server) handleTaskReschedule(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "tasks:write") {
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeErr(w, 400, "missing_field", "id required")
		return
	}
	var body rescheduleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "bad_request", err.Error())
		return
	}
	if body.Status == "" {
		writeErr(w, 400, "missing_field", "status required")
		return
	}
	if err := s.db.RescheduleTask(id, body.NextRun, body.Status); err != nil {
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// isChannelJID reports whether jid is a platform-channel JID (subject to the
// ingress ownership check) vs an internal scheme. web:/hook:/bare-folder JIDs
// address groups directly (web chat, timed, onbod) and carry no channel prefix,
// so they're exempt; everything with a "platform:" prefix is a channel JID.
func isChannelJID(jid string) bool {
	if strings.HasPrefix(jid, "web:") || strings.HasPrefix(jid, "hook:") {
		return false
	}
	return strings.Contains(jid, ":")
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
