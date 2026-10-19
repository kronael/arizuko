package routd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/resreg"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
)

// Deliverer is the egress half: routd writes the messages row first, then the
// Deliverer fans out to the owning adapter's POST /v1/send. Tests inject a fake.
type Deliverer interface {
	// Send delivers text threaded under replyToID/threadID; idempotencyKey is
	// the bot row's stable message_id so the adapter dedups. threadRoot, with
	// threadID empty, roots a NEW thread on that message id.
	Send(jid, text, replyToID, threadID, threadRoot, idempotencyKey string) (platformID string, err error)
	// SendOptions is Send carrying pickable answers; chanreg folds them into
	// the text for a channel without buttons.
	SendOptions(jid, text string, options []chanlib.Option, threadID, idempotencyKey string) (platformID string, err error)
	// SendVia is Send pinned to a named adapter — step 1 of the outbound
	// resolution order (spec 5/34-channel-protocol). Only POST /v1/outbound carries a pin: a JID
	// prefix can be owned by two adapter accounts, and ForJID's iteration order
	// is non-deterministic. Empty channel is exactly Send with no threading.
	SendVia(channel, jid, text string) (platformID string, err error)
	React(jid, platformID, reaction string) error
	Edit(jid, platformID, content string) error
	// CanEdit reports whether the channel owning jid advertises `edit`: the
	// capability map decides where a progress message may live, never a
	// platform error (routd/progress.go).
	CanEdit(jid string) bool
	Delete(jid, platformID string) error
	Pin(jid, platformID string) error
	Unpin(jid, platformID string, all bool) error
	// Typing is best-effort; web / no-presence channels are no-ops.
	Typing(jid string, on bool) error
	// Document delivers a file at path on the shared group volume both routd
	// and the adapter mount.
	Document(jid, path, name, caption, replyToID, threadID, idempotencyKey string) (platformID string, err error)
	// SendVoice delivers a synthesized voice note (Opus/Ogg); routd synthesizes
	// via TTS first (tts.go).
	SendVoice(jid, audioPath, caption, threadID string) (platformID string, err error)
	// The social/feed surface; SetSuggestions/SetName stage Slack assistant-pane
	// controls.
	Post(jid, content string, mediaPaths []string) (platformID string, err error)
	Forward(sourceMsgID, targetJID, comment string) (platformID string, err error)
	Quote(jid, sourceMsgID, comment string) (platformID string, err error)
	Repost(jid, sourceMsgID string) (platformID string, err error)
	Dislike(jid, platformID string) error
	SetSuggestions(jid string, prompts []core.PanePrompt) error
	SetName(jid, title string) error
	// RoundDone tells the web SSE channel a turn closed. chatJID is the turn's
	// chat JID VERBATIM — webd's hub keys on it as-is (BUGS L30).
	RoundDone(chatJID, turnID, status, errMsg string) error
	// FetchHistory proxies to the owning adapter's GET /v1/history; any error
	// makes the caller fall back to the local DB.
	FetchHistory(jid string, before time.Time, limit int) ([]byte, error)
}

// Verifier offline-verifies inbound bearer tokens against authd's keys. routd
// is a verifier, not a signer.
type Verifier interface {
	Verify(r *http.Request) (sub string, scope []string, folder string, err error)
}

// Server is routd's HTTP face: ingress + route/web-route/route-token CRUD +
// outbound passthrough. It hosts the per-turn agent MCP socket (mcp.go).
type Server struct {
	db          *DB
	loop        *Loop
	deliver     Deliverer
	verify      Verifier
	engagementT time.Duration
	webHost     string

	// Mirror proxyd's vhost config so get_web_presence reports a folder's
	// canonical host (spec 5/V-web-vhosts).
	hostingDomain string
	vhostAliases  map[string]string

	// nil → inspect_identity answers unclaimed.
	identity IdentityResolver

	// dashKeys gates GET /dash/routd/ on the proxyd-verified end-user identity,
	// not the /v1 bearer (spec 6/28); nil → local dev, gate open.
	dashKeys *auth.KeySet

	// nil → no prior sessions.
	sessions SessionResolver

	// SEND_DISABLED_GROUPS: muted folders whose outbound persists (status=sent)
	// but is NOT delivered. SEND_DISABLED_CHANNELS stays in the Deliverer.
	disabledGroups []string

	// Back the file-path agent tools (send_file, vhosts).
	groupsDir string
	webDir    string

	// Zero value leaves send_voice returning not-configured.
	tts ttsConfig

	// Observability only (audit-system.jl in DATA_DIR); nil → slog-only.
	audit *audit.Audit

	// Loaded once at boot, registered through every per-turn MCP socket;
	// nil/empty leaves the connector path off.
	connectors []ipc.ConnectorSpec

	// REST-descriptor tool catalog, registered per turn.
	extTools []ipc.ExtTool

	// reg==nil leaves the /v1/channels endpoints unmounted (pure REST tests).
	reg *chanreg.Registry
}

// DefaultEngagementTTL is the stay-in-conversation window (spec 5/G-engagement) when
// ENGAGEMENT_TTL is unset. The caller resolves that default before NewServer;
// an explicit zero disables automatic engagement.
const DefaultEngagementTTL = 30 * time.Minute

// NewServer wires the HTTP server. loop may be nil for pure REST tests.
func NewServer(db *DB, loop *Loop, deliver Deliverer, verify Verifier, engagementTTL time.Duration, webHost string) *Server {
	return &Server{db: db, loop: loop, deliver: deliver, verify: verify, engagementT: engagementTTL, webHost: webHost}
}

// SetDirs supplies the group + web roots the in-process MCP file-path tools
// (send_file, vhosts) resolve against.
func (s *Server) SetDirs(groupsDir, webDir string) {
	s.groupsDir = groupsDir
	s.webDir = webDir
}

// SetVhosts supplies HOSTING_DOMAIN + the WEB_VHOST_ALIASES host→folder map.
// routd never serves vhosts, it only reports them.
func (s *Server) SetVhosts(hostingDomain string, aliases map[string]string) {
	s.hostingDomain = hostingDomain
	s.vhostAliases = aliases
}

// webPresence is the single renderer feeding both the get_web_presence MCP
// tool and the /v1/web_presence REST twin.
func (s *Server) webPresence(folder string) ipc.WebPresence {
	return ipc.WebPresenceFor(folder, s.webHost, s.hostingDomain, s.vhostAliases)
}

// SetDisabledGroups supplies SEND_DISABLED_GROUPS.
func (s *Server) SetDisabledGroups(folders []string) { s.disabledGroups = folders }

// mutedGroup reports whether folder is in SEND_DISABLED_GROUPS.
func (s *Server) mutedGroup(folder string) bool {
	for _, f := range s.disabledGroups {
		if strings.EqualFold(f, folder) {
			return true
		}
	}
	return false
}

// SetTTS supplies the send_voice synthesis config; the zero value leaves voice off.
func (s *Server) SetTTS(c ttsConfig) { s.tts = c }

// SetAudit injects the audit writer so mutating MCP tool calls emit
// audit-system.jl events.
func (s *Server) SetAudit(a *audit.Audit) { s.audit = a }

// SetConnectors supplies the configured connector specs (LoadConnectors); every
// per-turn MCP socket discovers each one's catalog with the folder's credential.
func (s *Server) SetConnectors(c []ipc.ConnectorSpec) { s.connectors = c }

// SetExtTools supplies the REST-descriptor tool catalog (LoadExtProviders).
func (s *Server) SetExtTools(t []ipc.ExtTool) { s.extTools = t }

// SetIdentityResolver wires the authd identity client backing inspect_identity.
func (s *Server) SetIdentityResolver(r IdentityResolver) { s.identity = r }

// resolveIdentity backs StoreFns.GetIdentityForSub. inspect_identity is
// advisory, so a resolver error renders unclaimed — but logged, because "authd
// did not answer" and "nobody claims this sub" read identically in the tool's
// output (BUGS N2).
func (s *Server) resolveIdentity(sub string) (ipc.Identity, []string, bool) {
	if s.identity == nil {
		return ipc.Identity{}, nil, false
	}
	id, subs, ok, err := s.identity.Resolve(sub)
	if err != nil {
		slog.Warn("inspect_identity: authd could not answer; rendering unclaimed",
			"sub", sub, "err", err)
		return ipc.Identity{}, nil, false
	}
	return id, subs, ok
}

// SetSessionResolver wires the runed session client backing inspect_session.
func (s *Server) SetSessionResolver(r SessionResolver) { s.sessions = r }

// recentSessions backs StoreFns.RecentSessions via runed's
// GET /v1/spawn_sessions/recent; a nil resolver returns nil.
func (s *Server) recentSessions(folder string, n int) []core.SessionRecord {
	if s.sessions == nil {
		return nil
	}
	return s.sessions.RecentSessions(folder, n)
}

// SetDashKeySet wires the operator gate for GET /dash/routd/; nil in local dev
// and tests, where the gate falls open exactly as every /v1 gate does.
func (s *Server) SetDashKeySet(ks *auth.KeySet) { s.dashKeys = ks }

// Handler builds the routed mux. GET /health and /openapi.json are public;
// everything else is bearer-gated by the Verifier.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	mux.HandleFunc("POST /v1/messages", s.handleMessages)
	mux.HandleFunc("POST /v1/outbound", s.handleOutbound)
	s.mountRoutes(mux)
	s.mountWebRoutes(mux)
	s.mountRouteTokens(mux)
	s.mountGitHubSources(mux)
	s.mountGDriveFiles(mux)
	s.mountGroups(mux)
	// Read-only by design: install/upgrade/remove writes host files and
	// restarts sidecars, so it stays the `arizuko packages` CLI (spec 5/28-packages).
	s.mountInstalledPackages(mux)
	s.mountAudit(mux)
	s.mountDeadLetters(mux)
	// Verdicts funnel to the same core the chat /approve command runs (spec 5/19).
	s.mountPendingActions(mux)
	// The REST twin of routd's in-process MCP StoreFns.
	mux.HandleFunc("GET /v1/messages/inspect", s.handleInspectMessages)
	mux.HandleFunc("GET /v1/messages/thread", s.handleThreadMessages)
	mux.HandleFunc("GET /v1/messages/find", s.handleFindMessages)
	mux.HandleFunc("GET /v1/routing/resolve", s.handleRoutingResolve)
	mux.HandleFunc("GET /v1/routing/errored", s.handleErroredChats)
	mux.HandleFunc("GET /v1/routing/unrouted", s.handleUnroutedChats)
	mux.HandleFunc("GET /v1/routing/answering", s.handleAnswering)
	mux.HandleFunc("GET /v1/engagement", s.handleEngagementGet)
	mux.HandleFunc("POST /v1/engagement", s.handleEngagementSet)
	// topic_sessions, NOT sessions: `sessions` is authd's wire identity
	// (BUGS F46, spec 5/5-worlds-agents-sessions § Tier 3).
	mux.HandleFunc("GET /v1/topic_sessions", s.handleSessionGet)
	// Operator-cockpit aggregates (6/28, BUGS L142). REST-only on purpose: as
	// resreg resources these would become agent MCP tools.
	mux.HandleFunc("GET /v1/sessions/counts", s.handleSessionCounts)
	mux.HandleFunc("GET /v1/messages/volume", s.handleMessageVolume)
	mux.HandleFunc("GET /v1/messages/activity", s.handleActivity)
	mux.HandleFunc("GET /v1/principals", s.handlePrincipals)
	mux.HandleFunc("GET /v1/users/{sub}/scopes", s.handleUserScopes)
	s.mountACL(mux)
	// The account side of unpair (spec 5/31-identity-pairing): the caller's verified sub must
	// BE the edge's parent.
	s.mountMembership(mux)
	// /v1/secrets is a forwarder (no resreg tx/audit) so the plaintext never
	// lands in audit_log; the sealing s.db.SetSecret/DeleteSecret own the
	// write. NO read twin — a sealed value must never appear in any read surface.
	s.mountSecrets(mux)
	mux.HandleFunc("POST /v1/pane", s.handlePaneSet)
	// The fire-loop control plane (due/runlog/reschedule) + run-log readers
	// stay hand-rolled: they serve timed, not resource CRUD.
	s.mountTasks(mux)
	mux.HandleFunc("GET /v1/tasks/runs", s.handleAllRunLogs)
	mux.HandleFunc("GET /v1/tasks/due", s.handleTasksDue)
	mux.HandleFunc("POST /v1/tasks/runlog", s.handleTaskRunLog)
	mux.HandleFunc("GET /v1/tasks/{id}/runs", s.handleTaskRunLogs)
	mux.HandleFunc("POST /v1/tasks/{id}/reschedule", s.handleTaskReschedule)
	mux.HandleFunc("GET /v1/cost", s.handleCostGet)
	mux.HandleFunc("GET /v1/cost/summary", s.handleCostSummary)
	s.mountChannels(mux)
	// The operator dashboard (spec 6/28): gated by the proxyd-stamped operator
	// identity, not the /v1 bearer.
	mux.HandleFunc("GET /dash/routd/", s.handleDash)
	mux.HandleFunc("POST /dash/routd/retry", s.handleDashRetry)
	mux.HandleFunc("POST /dash/routd/replay", s.handleDashReplay)
	return mux
}

// authz verifies the bearer and, when scopes are required, that it carries one
// (any-of). verify==nil is open (local-dev): ok=true, empty sub/folder. Fails
// CLOSED. Service subs carry the daemon's scopes like any other token; there
// is no implicit bypass.
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

// hasAnyScope reports whether held grants any wanted "resource:verb" scope. A
// held "resource:*" covers any verb (auth.HasScope); an exact string also
// matches, which covers the "resource:verb:own_group" form.
func hasAnyScope(held, wanted []string) bool {
	for _, w := range wanted {
		if i := strings.IndexByte(w, ':'); i > 0 {
			if auth.HasScope(held, w[:i], w[i+1:]) {
				return true
			}
		}
		if slices.Contains(held, w) {
			return true
		}
	}
	return false
}

// restCaller builds the REST Caller for a shared resource handler: identity
// via the Verifier, Folder from target (the JWT folder when target is nil),
// held scopes and the JWT folder in Claims. A nil Verifier is open, as s.authz.
func (s *Server) restCaller(target func(r *http.Request, jwtFolder string) string) resreg.CallerFromHTTPFunc {
	return func(r *http.Request) (resreg.Caller, error) {
		var sub, jwtFolder string
		var scope []string
		if s.verify != nil {
			var err error
			sub, scope, jwtFolder, err = s.verify.Verify(r)
			if err != nil {
				return resreg.Caller{}, err
			}
		}
		folder := jwtFolder
		if target != nil {
			folder = target(r, jwtFolder)
		}
		return resreg.Caller{
			Sub:    sub,
			Folder: folder,
			Claims: map[string]string{
				resreg.ScopeClaimKey: strings.Join(scope, " "),
				"jwt_folder":         jwtFolder,
			},
		}, nil
	}
}

// ownsFolder reports whether the token's folder claim owns target (equal or
// ancestor). An empty token folder (open mode) owns everything; an empty
// target is owned by anyone. Fails CLOSED outside the token's subtree.
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

// ownsJID mirrors ipc.authorizeJID's resolution EXACTLY so the REST and MCP
// faces gate identically: the jid's routing target, else the web:<folder>
// binding; allow when that target is in the token's subtree OR the token's
// folder is itself a route target for jid. Empty token folder owns everything.
func (s *Server) ownsJID(tokenFolder, jid string) bool {
	if tokenFolder == "" {
		return true
	}
	target := s.db.DefaultFolderForJID(jid)
	// No route row: read the folder the MINT recorded, never the JID grammar —
	// `web:T/S` is byte-identical to a plain token at a folder named `T/S`
	// (BUGS L4). The grammar is the last resort.
	if target == "" {
		target = s.db.RouteTokenTarget(jid)
	}
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

// denyCrossFolder is the REST twin of ipc.authorizeJID's error.
func denyCrossFolder(w http.ResponseWriter, jid string) {
	writeErr(w, 403, "forbidden", "chat "+jid+" is outside your folder")
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request) {
	sub, _, ok := s.authz(w, r, "messages:write")
	if !ok {
		return
	}
	// The minted id's adapter name is the verified caller (service:<adapter>);
	// "adapter" when unverified.
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
	// A platform-scheme inbound must land under a registered channel's
	// prefixes. Internal schemes (web:/hook:/bare-folder) are exempt; unverified
	// (local dev) falls back to any registered owner.
	if s.reg != nil && isChannelJID(m.ChatJID) {
		if sub != "" {
			entry := s.reg.ByPrincipal(sub)
			if entry != nil && !entry.Owns(m.ChatJID) {
				writeErr(w, 400, "jid_prefix_mismatch", "adapter doesn't own "+m.ChatJID)
				return
			}
		} else if s.reg.ForJID(m.ChatJID) == nil {
			writeErr(w, 400, "jid_prefix_mismatch", "no registered channel owns "+m.ChatJID)
			return
		}
	}
	// Sender ownership, NOT nested in the branch above: a web:/hook: ChatJID
	// skips it, and Sender is authorization-bearing (steer.go feeds it to
	// Authorize), so asserting another platform's user is an escalation. The
	// test is the scheme, not the full prefix: senders are "telegram:user/<id>",
	// not chat prefixes. Only QUALIFIED senders: a bare one cannot collide with
	// an OAuth sub. "anon:" is exempt for the same reason — webd holds a
	// registry entry, and the guard took web chat down on krons (2026-08-05).
	if s.reg != nil && sub != "" && strings.Contains(m.Sender, ":") && !strings.HasPrefix(m.Sender, "anon:") {
		if entry := s.reg.ByPrincipal(sub); entry != nil && !entry.OwnsScheme(m.Sender) {
			writeErr(w, 400, "sender_scheme_mismatch",
				"adapter may not assert sender "+m.Sender)
			return
		}
	}
	// X-Idempotency-Key is honored ONLY when id is absent: routd mints
	// id=<adapter>-<key> so the two keys collapse. Both together is ambiguous.
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
	verb := m.Verb
	if verb == "" {
		verb = "message"
	}
	// The parent settles three things: a reply with no topic inherits the
	// parent's (BEFORE promotion, so the thread-participation check sees it); a
	// reply with no excerpt gets the parent's sender and text; a reply to a bot
	// row is a mention.
	var parent core.Message
	hasParent := false
	if m.ReplyTo != "" {
		parent, hasParent = s.db.MessageByID(m.ChatJID, m.ReplyTo)
	}
	if m.Topic == "" && hasParent {
		m.Topic = parent.Topic
	}
	if m.ReplyToText == "" && hasParent {
		m.ReplyToText, m.ReplyToSender = parent.Content, parent.Name
		if parent.Name == "" {
			m.ReplyToSender = parent.Sender
		}
	}
	m.ReplyToText = truncate(m.ReplyToText, replyExcerptChars)
	canPromoteReply := true
	github := strings.HasPrefix(m.ChatJID, "github:")
	if github {
		jid, valid := chanlib.ParseGitHubJID(m.ChatJID)
		canPromoteReply = valid && jid.Kind == "pr" && (verb == "message" || verb == "mention")
	}
	directReply := canPromoteReply && hasParent && parent.BotMsg &&
		(verb != "message" || m.ReplyTo != m.Topic ||
			core.JidPlatform(m.ChatJID) != "slack")
	threadMention := false
	if !github && verb != "untrusted" && verb != "mention" && !directReply && m.Topic != "" {
		stopped, err := s.db.store().ThreadDisengaged(m.ChatJID, m.Topic)
		if err != nil {
			writeErr(w, 500, "store_error", err.Error())
			return
		}
		root, exists := s.db.MessageByID(m.ChatJID, m.Topic)
		threadMention = !stopped && exists && root.BotMsg
	}
	if verb != "untrusted" && (directReply || threadMention) {
		verb = "mention"
	}
	// A ✅/❌ reaction on a held notice IS the verdict: rewrite it into the
	// command it stands for — one parser, one operator gate. HERE, not in steer,
	// which runs after route resolution: a reaction on a mention-only channel
	// never gets that far.
	if cmd := s.db.VerdictForReaction(m.ChatJID, m.ReplyTo, m.Content); cmd != "" {
		m.Content, verb = cmd, "mention"
	}
	row := buildMessageRow(m, ts, verb)
	// A held tool blocks the agent worker. Its verdict must not queue behind it.
	if s.loop != nil && s.loop.handleVerdictCommand(m.ChatJID, row) {
		writeJSON(w, 200, apiv1.MessageAck{OK: true, ID: m.ID})
		return
	}
	// Engagement is NOT committed at ingress: the folder is unknown until route
	// resolution. A pre-PutMessage claim with an empty folder would make
	// Engaged return ("", true) and misroute.
	if err := s.db.PutMessage(row); err != nil {
		writeErr(w, 500, "store_error", err.Error())
		return
	}
	// A classification is written only by a caller that gave one (api/v1
	// § Message.IsGroup).
	if m.IsGroup != nil {
		// the chat is stored and routed either way; a flag write that fails is
		// restated by the adapter's next inbound
		_ = s.db.SetChatIsGroup(m.ChatJID, *m.IsGroup)
	}
	if s.loop != nil {
		s.loop.Enqueue(m.ChatJID)
	}
	writeJSON(w, 200, apiv1.MessageAck{OK: true, ID: m.ID})
}

func (s *Server) handleOutbound(w http.ResponseWriter, r *http.Request) {
	sub, _, ok := s.authz(w, r, "messages:write")
	if !ok {
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
	// Step 1 of the outbound resolution order (spec 5/34-channel-protocol): an explicit channel
	// wins. "if registered" is checked HERE and refused loudly — Registry.Resolve
	// falls back to ForJID for an unknown name and would silently deliver
	// through the adapter the caller pinned away from.
	if req.Channel != "" {
		if s.reg == nil || s.reg.Get(req.Channel) == nil {
			writeErr(w, 400, "unknown_channel",
				"channel "+req.Channel+" is not registered; omit `channel` to resolve by jid")
			return
		}
	}
	// A daemon's message is a messages row like the agent's: routd is the sole
	// appender, and a send with no row is invisible to find_messages, the
	// transcript and dead_letters. A refused send lands `failed` with
	// delivery_attempts=0, so the pending sweep never retries it.
	row := core.Message{
		ID: "out-" + randHex(8), ChatJID: req.JID, Sender: sub, Content: req.Text,
		Timestamp: time.Now().UTC(), BotMsg: true, FromMe: true,
	}
	status := core.MessageStatusSent
	pid, sendErr := s.deliver.SendVia(req.Channel, req.JID, req.Text)
	if sendErr != nil {
		status = core.MessageStatusFailed
	} else {
		row.PlatformID = pid
	}
	// A store failure after a successful send is logged, not returned: a 5xx
	// makes the caller send it twice.
	if err := s.db.PutOutbound(row, status); err != nil {
		slog.Error("outbound row not stored", "jid", req.JID, "sender", sub, "status", status, "err", err)
	}
	if sendErr != nil {
		writeErr(w, 502, "delivery_failed", sendErr.Error())
		return
	}
	writeJSON(w, 200, apiv1.OK{OK: true})
}

// handleUserScopes serves the login-time scope snapshot authd pulls at session
// issuance: 200 {"scope":[...],"folder":"..."} when the sub holds grants, 404
// no_grants when it holds none.
func (s *Server) handleUserScopes(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r, "grants:read") {
		return
	}
	sub := r.PathValue("sub")
	scope, err := s.db.UserScopes(sub)
	if err != nil {
		// NOT 404 no_grants: authd would snapshot "this user holds nothing" from
		// a broken read and mint a scopeless session (BUGS A4).
		writeErr(w, 500, "db_error", err.Error())
		return
	}
	if len(scope) == 0 {
		writeErr(w, 404, "no_grants", "no grants for sub "+sub)
		return
	}
	// folder is the single-subtree bound for the minted token: the lone scope
	// when the sub holds exactly one, else empty.
	folder := ""
	if len(scope) == 1 {
		folder = scope[0]
	}
	writeJSON(w, 200, map[string]any{"scope": scope, "folder": folder})
}

// paneSetBody is the POST /v1/pane payload: slakd's three Slack-pane writes.
// op "open" upserts the row; "context" sets the (team,user,thread) context
// (empty jid clears it); "" sets the context by channel.
type paneSetBody struct {
	Op        string `json:"op"`
	TeamID    string `json:"team_id"`
	UserID    string `json:"user_id"`
	ThreadTS  string `json:"thread_ts"`
	ChannelID string `json:"channel_id"`
	JID       string `json:"jid"`
}

// handlePaneSet performs slakd's pane write.
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

// dueTask is the GET /v1/tasks/due row: exactly what timed's fire loop needs
// to enqueue a message + reschedule.
type dueTask struct {
	ID          string `json:"id"`
	ChatJID     string `json:"chat_jid"`
	Prompt      string `json:"prompt"`
	Cron        string `json:"cron"`
	ContextMode string `json:"context_mode"`
	// NextRun is the scheduled time this claim FIRES FOR, unchanged by the
	// claim: timed keys the message id on it, so a retry after a lost response
	// mints the SAME id and the id-PK dedups (BUGS L33). RFC3339 UTC so the
	// round-trip through reschedule is byte-stable.
	NextRun string `json:"next_run"`
}

// handleTasksDue atomically claims (marks 'firing') and returns the tasks whose
// next_run has passed — the read half of timed's fire loop.
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
		next := ""
		if t.NextRun != nil {
			next = t.NextRun.UTC().Format(time.RFC3339)
		}
		out[i] = dueTask{ID: t.ID, ChatJID: t.ChatJID, Prompt: t.Prompt,
			Cron: t.Cron, ContextMode: t.ContextMode, NextRun: next}
	}
	writeJSON(w, 200, map[string]any{"tasks": out})
}

// taskRunLogBody is the POST /v1/tasks/runlog payload; duration_ms is the fire
// latency.
type taskRunLogBody struct {
	TaskID     string `json:"task_id"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	DurationMS int64  `json:"duration_ms"`
}

// handleTaskRunLog appends one task_run_logs row — the write half of timed's
// fire loop.
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

// rescheduleBody is the POST /v1/tasks/{id}/reschedule payload: the next_run
// timed computed + the target status. An empty next_run clears it (one-shot
// completion).
type rescheduleBody struct {
	NextRun string `json:"next_run"`
	Status  string `json:"status"`
}

// handleTaskReschedule sets a fired task's next_run + status. timed computes
// next_run and passes status so routd stays a boring writer: recurring →
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

// isChannelJID reports whether jid is a platform-channel JID, subject to the
// ingress ownership check. web:/hook:/bare-folder JIDs address groups directly
// and carry no channel prefix, so they are exempt.
func isChannelJID(jid string) bool {
	if strings.HasPrefix(jid, "web:") || strings.HasPrefix(jid, "hook:") {
		return false
	}
	return strings.Contains(jid, ":")
}

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
