package main

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
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
	st          *store.Store
	hub         *hub
	rc          *chanlib.RouterClient
	proxyd      *proxydClient
	requireUser func(http.HandlerFunc) http.HandlerFunc
}

func newServer(cfg config, st *store.Store, h *hub, rc *chanlib.RouterClient, ks *auth.KeySet) *server {
	return &server{
		cfg: cfg, st: st, hub: h, rc: rc,
		proxyd: newProxydClient(cfg.proxydURL, cfg.hmacSecret),
		// Soak (spec 5/1 § cutover): accept HMAC X-User-Sig OR an authd ES256
		// bearer. ks is nil unless AUTHD_URL is set → identical to
		// RequireSigned(hmacSecret) in the live HMAC-only deployment.
		requireUser: auth.RequireSignedOrBearer(cfg.hmacSecret, ks),
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

	mux.HandleFunc("POST /send", chanlib.Auth(s.cfg.channelSecret, s.handleSend))
	mux.HandleFunc("POST /typing", chanlib.Auth(s.cfg.channelSecret, s.handleTyping))
	mux.HandleFunc("POST /v1/round_done", chanlib.Auth(s.cfg.channelSecret, s.handleRoundDone))
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

// loadHMACSecret returns the proxyd-shared secret; falls back to a random value
// so sig-checks fail closed when the env var is unset.
func loadHMACSecret() string {
	if v := os.Getenv("PROXYD_HMAC_SECRET"); v != "" {
		return v
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err == nil {
		slog.Warn("PROXYD_HMAC_SECRET unset in webd — signed-header verification will fail until set on both")
		return hex.EncodeToString(b[:])
	}
	return ""
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
