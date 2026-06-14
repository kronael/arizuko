package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

//go:embed static
var staticFS embed.FS

const (
	maxJSONBody = 1 << 20  // 1 MiB — generous for MCP/channel callbacks
	maxFormBody = 64 << 10 // 64 KiB — route-token form posts
	maxTopicLen = 128      // prevents hub map growth from attacker-chosen keys
)

type server struct {
	cfg         config
	st          *store.Store // messages.db: audit_log only (legacy, frozen for messages/turns/tokens)
	stRoutd     *store.Store // routd.db: groups, messages, turn_results, route_tokens (live)
	hub         *hub
	rc          *chanlib.RouterClient
	proxyd      *proxydClient
	ks          *auth.KeySet // ES256 JWKs; nil → HMAC-only (local dev)
	requireUser func(http.HandlerFunc) http.HandlerFunc
	limiter     *rateLimiter
}

func newServer(cfg config, st, stRoutd *store.Store, h *hub, rc *chanlib.RouterClient, ks *auth.KeySet, svc *auth.TokenSource) *server {
	s := &server{
		cfg: cfg, st: st, stRoutd: stRoutd, hub: h, rc: rc,
		proxyd:  newProxydClient(cfg.proxydURL, svc),
		ks:      ks,
		limiter: newRateLimiter(cfg.rateHookPerMin, cfg.rateWebPerMin),
	}
	s.requireUser = s.requireIdentified
	return s
}

// requireIdentified gates a handler on a trustworthy proxyd-stamped identity.
// Post-flip (HMAC retire step 2) proxyd no longer signs X-User-Sig; it proves
// the channel with its own service:proxyd ES256 bearer and stamps the verified
// user into X-User-*. So identity is the STAMPED header, authenticated by the
// bearer — NOT the bearer's own sub (which is service:proxyd, not the user).
// auth.RequireSignedOrBearer can't be used here: it treats the bearer AS the
// identity and would clobber X-User-Sub with service:proxyd. With ks nil (local
// dev / pre-flip) identified() falls back to the HMAC sig, so legacy behaviour
// is unchanged.
func (s *server) requireIdentified(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.identified(r) {
			slog.Warn("webd: identity verify failed",
				"path", r.URL.Path, "attempted_sub", r.Header.Get("X-User-Sub"))
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	// /assets/* — shared static files baked into the webd binary
	// (currently: the chat-widget SDK). CORS permissive.
	mux.HandleFunc("GET /assets/{path...}", s.handleAssets)
	mux.HandleFunc("OPTIONS /assets/{path...}", s.handleAssets)

	mux.HandleFunc("POST /send", chanlib.Auth(s.handleSend))
	mux.HandleFunc("POST /typing", chanlib.Auth(s.handleTyping))
	mux.HandleFunc("POST /v1/round_done", chanlib.Auth(s.handleRoundDone))
	mux.HandleFunc("GET /health", s.handleHealth)
	// webd hosts the chat widget + MCP forwarder; routes resource is
	// forwarded to proxyd, no owned cold-tier resources. Doc lists the
	// public surface only.
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("webd", []string{}))

	// Route tokens (spec 5/W). Two URL prefixes share one set of
	// handlers; kind metadata is for the agent, not a URL gate. Both
	// /chat/* and /hook/* paths get permissive CORS — the route token
	// IS the public credential.
	withCORS := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, chatCORS(h))
	}
	// /chat/<token>/ — human chat widget + SSE.
	withCORS("GET /chat/{token}/{$}", s.handleChatTokenRoot)
	withCORS("GET /chat/{token}/config", s.handleChatTokenConfig)
	withCORS("POST /chat/{token}", s.handleChatTokenPost)
	withCORS("POST /chat/{token}/", s.handleChatTokenPost)
	withCORS("POST /chat/{token}/mcp", s.handleChatTokenMCP)
	withCORS("GET /chat/{token}/{topic}/messages", s.handleChatTokenHistory)
	withCORS("GET /chat/stream", s.handleRouteTokenStream)
	withCORS("GET /chat/{token}/{id}", s.handleTurnSnapshot)
	withCORS("GET /chat/{token}/{id}/status", s.handleTurnStatus)
	withCORS("GET /chat/{token}/{id}/sse", s.handleTurnSSE)
	// /hook/<token> — fire-and-forget webhook ingest.
	withCORS("POST /hook/{token}", s.handleHookTokenPost)
	// Preflight covers both prefixes.
	mux.Handle("OPTIONS /chat/", chatCORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	mux.Handle("OPTIONS /hook/", chatCORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))
	// Legacy /slink/<token>/... → 301 to /chat/<token>/...
	mux.HandleFunc("GET /slink/{token}/{rest...}", s.handleSlinkRedirect)
	mux.HandleFunc("POST /slink/{token}/{rest...}", s.handleSlinkRedirect)
	mux.HandleFunc("GET /slink/{token}", s.handleSlinkRedirect)
	mux.HandleFunc("POST /slink/{token}", s.handleSlinkRedirect)

	// MCP: single per-instance, user-grant-gated
	mux.HandleFunc("POST /mcp", s.requireUser(s.handleMCP))
	mux.HandleFunc("GET /mcp", s.requireUser(s.handleMCP))
	mux.HandleFunc("DELETE /mcp", s.requireUser(s.handleMCP))

	// User dashboard /me/*
	mux.HandleFunc("GET /me/", s.requireUser(s.handleMeIndex))
	mux.HandleFunc("GET /me/chats", s.requireUser(s.handleMeChats))
	mux.HandleFunc("GET /me/chats/new", s.requireUser(s.handleMeNewChat))
	mux.HandleFunc("POST /me/chats/new", s.requireUser(s.handleMeNewChatPost))
	mux.HandleFunc("GET /me/settings", s.requireUser(s.handleMeSettings))
	mux.HandleFunc("PATCH /me/settings", s.requireUser(s.handleMeSettingsPatch))
	mux.HandleFunc("GET /me/x/folders", s.requireUser(s.handleMeXFolders))
	mux.HandleFunc("GET /me/x/chats", s.requireUser(s.handleMeXChats))
	mux.HandleFunc("GET /me/x/thread", s.requireUser(s.handleMeXThread))
	// /me/chats/{folder...} and /me/folders/{folder...} use catch-all path values;
	// sub-paths (send, sse) are handled by checking suffixes in meFolderTopic.
	mux.HandleFunc("GET /me/chats/{folder...}", s.requireUser(s.handleMeThreadOrList))
	mux.HandleFunc("POST /me/chats/{folder...}", s.requireUser(s.handleMeThreadOrList))
	mux.HandleFunc("GET /me/folders/{folder...}", s.requireUser(s.handleMeFolderOrFiles))

	mux.HandleFunc("GET /{$}", s.requireUser(s.handleGroupsPage))
	mux.HandleFunc("GET /panel/{folder...}", s.requireFolder(s.handleChatPage))

	mux.HandleFunc("GET /api/groups", s.requireUser(s.handleAPIGroups))
	mux.HandleFunc("GET /api/groups/{rest...}", s.requireUser(s.routeAPIGroups))
	mux.HandleFunc("POST /api/groups/{rest...}", s.requireUser(s.routeAPIGroups))

	mux.HandleFunc("GET /x/groups", s.requireUser(s.handleXGroups))
	mux.HandleFunc("GET /x/groups/{rest...}", s.requireUser(s.routeXGroups))

	return chanlib.LogMiddleware(mux)
}

func userGroups(r *http.Request) []string {
	var out []string
	if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
		_ = json.Unmarshal([]byte(hdr), &out)
	}
	return out
}

func userAllowedFolder(groups []string, folder string) bool {
	for _, f := range groups {
		if f == "**" || f == folder || strings.HasPrefix(folder, f+"/") {
			return true
		}
	}
	return false
}

func (s *server) requireFolder(next http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		folder := folderParam(r)
		if !userAllowedFolder(userGroups(r), folder) {
			slog.Warn("folder access denied",
				"sub", userSub(r), "folder", folder, "path", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// splitFolderSuffix splits "atlas/content/topics" → ("atlas/content", "topics").
// Bare "/topics" is rejected — prevents folder="" from bypassing ACL.
func splitFolderSuffix(rest string) (string, string) {
	for _, suffix := range []string{"/topics", "/messages", "/typing"} {
		if !strings.HasSuffix(rest, suffix) {
			continue
		}
		folder := rest[:len(rest)-len(suffix)]
		if folder == "" {
			continue
		}
		return folder, suffix[1:]
	}
	return rest, ""
}

func (s *server) routeAPIGroups(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	folder, suffix := splitFolderSuffix(rest)
	r.SetPathValue("folder", folder)
	switch {
	case suffix == "topics":
		s.requireFolder(s.handleAPITopics)(w, r)
	case suffix == "messages" && r.Method == http.MethodPost:
		s.requireFolder(s.handleAPIMessagesPost)(w, r)
	case suffix == "messages":
		s.requireFolder(s.handleAPIMessages)(w, r)
	case suffix == "typing" && r.Method == http.MethodPost:
		s.requireFolder(s.handleAPITyping)(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (s *server) routeXGroups(w http.ResponseWriter, r *http.Request) {
	rest := r.PathValue("rest")
	folder, suffix := splitFolderSuffix(rest)
	r.SetPathValue("folder", folder)
	switch suffix {
	case "topics":
		s.requireFolder(s.handleXTopics)(w, r)
	case "messages":
		s.requireFolder(s.handleXMessages)(w, r)
	default:
		http.NotFound(w, r)
	}
}

func userSub(r *http.Request) string  { return r.Header.Get("X-User-Sub") }
func userName(r *http.Request) string { return r.Header.Get("X-User-Name") }

// identified reports whether the stamped X-User-Sub on r is trustworthy. proxyd
// is the sole ingress: it strips client identity headers, authenticates the
// user, re-stamps X-User-* and proves the channel with its own service:proxyd
// ES256 bearer. The stamp is trusted ONLY on that transit proof — auth.ProxydTransit
// pins sub==service:proxyd so a caller reaching webd directly with a different
// valid authd token cannot forge X-User-*. With ks nil (local dev, AUTHD_URL
// unset) there is no proxyd to forge through and no JWKS to verify, so the
// stamped header is trusted, matching every other daemon's no-verifier path.
func (s *server) identified(r *http.Request) bool {
	if r.Header.Get("X-User-Sub") == "" {
		return false
	}
	if s.ks == nil {
		return true // local dev
	}
	return auth.ProxydTransit(r, s.ks)
}

// chatTransit reports whether the X-Chat-Token / X-Folder headers were stamped
// by proxyd (route-token → folder binding, spec 5/W). proxyd is the sole setter
// — it strips any client-supplied X-Chat-* on ingress, resolves the token to a
// folder from route_tokens, stamps both, and proves the request transited it
// with its service:proxyd bearer. The caller then checks X-Folder matches the
// requested folder. ks nil (local dev, no proxyd) trusts the header, matching
// identified(). Replaces the X-Chat-Sig HMAC (HMAC retire step 5).
func (s *server) chatTransit(r *http.Request) bool {
	if r.Header.Get("X-Chat-Token") == "" || r.Header.Get("X-Folder") == "" {
		return false
	}
	if s.ks == nil {
		return true // local dev
	}
	return auth.ProxydTransit(r, s.ks)
}

func folderParam(r *http.Request) string {
	return strings.TrimPrefix(r.PathValue("folder"), "/")
}

// handleSlinkRedirect 301s legacy /slink/<token>/... → /chat/<token>/...
// One-pass back-compat for already-pasted slink URLs (spec 5/W cutover).
func (s *server) handleSlinkRedirect(w http.ResponseWriter, r *http.Request) {
	tail := strings.TrimPrefix(r.URL.Path, "/slink/")
	target := "/chat/" + tail
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, target, http.StatusMovedPermanently)
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{
		"status": "ok", "name": "web", "jid_prefixes": []string{"web:"},
	})
}

func messageRole(m core.Message) string {
	if m.BotMsg {
		return "assistant"
	}
	return "user"
}
