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
	"github.com/kronael/arizuko/store"
)

//go:embed static
var staticFS embed.FS

const (
	maxJSONBody = 1 << 20  // 1 MiB — generous for MCP/channel callbacks
	maxFormBody = 64 << 10 // 64 KiB — slink form posts
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

func newServer(cfg config, st *store.Store, h *hub, rc *chanlib.RouterClient) *server {
	return &server{
		cfg: cfg, st: st, hub: h, rc: rc,
		proxyd:      newProxydClient(cfg.proxydURL, cfg.hmacSecret),
		requireUser: auth.RequireSigned(cfg.hmacSecret),
	}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()

	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	// /assets/* — shared static files baked into the webd binary
	// (currently: the slink SDK). CORS permissive; see specs/1/Z2-slink-sdk.md.
	mux.HandleFunc("GET /assets/{path...}", s.handleAssets)
	mux.HandleFunc("OPTIONS /assets/{path...}", s.handleAssets)

	mux.HandleFunc("POST /send", chanlib.Auth(s.cfg.channelSecret, s.handleSend))
	mux.HandleFunc("POST /typing", chanlib.Auth(s.cfg.channelSecret, s.handleTyping))
	mux.HandleFunc("POST /v1/round_done", chanlib.Auth(s.cfg.channelSecret, s.handleRoundDone))
	mux.HandleFunc("GET /health", s.handleHealth)

	// slink: token-gated, see specs/1/W-slink.md and specs/1/Z-slink-widget.md.
	// All /slink/* routes get CORS headers via slinkCORS (token is public credential).
	slink := func(pattern string, h http.HandlerFunc) {
		mux.Handle(pattern, slinkCORS(h))
	}
	slink("GET /slink/{token}", s.handleSlinkRoot)
	slink("GET /slink/{token}/chat", s.handleSlinkChat)
	slink("GET /slink/{token}/config", s.handleSlinkConfig)
	slink("POST /slink/{token}", s.handleSlinkPost)
	slink("POST /slink/{token}/mcp", s.handleSlinkMCP)
	slink("GET /slink/stream", s.handleSlinkStream)
	slink("GET /slink/{token}/{id}", s.handleTurnSnapshot)
	slink("GET /slink/{token}/{id}/status", s.handleTurnStatus)
	slink("GET /slink/{token}/{id}/sse", s.handleTurnSSE)
	// Preflight: cover every /slink/* path with one pattern.
	mux.Handle("OPTIONS /slink/", slinkCORS(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})))

	// MCP: single per-instance, user-grant-gated
	mux.HandleFunc("POST /mcp", s.requireUser(s.handleMCP))
	mux.HandleFunc("GET /mcp", s.requireUser(s.handleMCP))
	mux.HandleFunc("DELETE /mcp", s.requireUser(s.handleMCP))

	mux.HandleFunc("GET /{$}", s.requireUser(s.handleGroupsPage))
	mux.HandleFunc("GET /chat/{folder...}", s.requireFolder(s.handleChatPage))

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
