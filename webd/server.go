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

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/store"
)

//go:embed static
var staticFS embed.FS

// maxJSONBody caps decoded JSON payload size for defense against unbounded
// client bodies. 1 MiB is generous for MCP/channel callbacks.
const maxJSONBody = 1 << 20

// maxFormBody caps form-encoded POST /slink/<token> bodies.
const maxFormBody = 64 << 10

// maxTopicLen bounds attacker-chosen topic strings which otherwise grow
// the hub map indefinitely.
const maxTopicLen = 128

type server struct {
	cfg config
	st  *store.Store
	hub *hub
	rc  *chanlib.RouterClient
}

func newServer(cfg config, st *store.Store, h *hub, rc *chanlib.RouterClient) *server {
	return &server{cfg: cfg, st: st, hub: h, rc: rc}
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()

	// Static files.
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(sub))))

	// Channel callbacks from gated (authenticated with channel secret).
	mux.HandleFunc("POST /send", chanlib.Auth(s.cfg.channelSecret, s.handleSend))
	mux.HandleFunc("POST /typing", chanlib.Auth(s.cfg.channelSecret, s.handleTyping))
	mux.HandleFunc("GET /health", s.handleHealth)

	// Public slink endpoints (token-gated internally).
	mux.HandleFunc("GET /slink/{token}", s.handleSlinkPage)
	mux.HandleFunc("POST /slink/{token}", s.handleSlinkPost)
	mux.HandleFunc("GET /slink/stream", s.handleSlinkStream)

	// MCP streamable-HTTP endpoint — single per-instance, JWT-gated. The
	// authed user can reach any folder in their user_groups ACL; each
	// tool takes `folder` as an arg and checks grants via userGroups(r).
	mux.HandleFunc("POST /mcp", s.requireUser(s.handleMCP))
	mux.HandleFunc("GET /mcp", s.requireUser(s.handleMCP))
	mux.HandleFunc("DELETE /mcp", s.requireUser(s.handleMCP))

	// Private: full pages (proxyd has already validated auth, injects X-User-Sub).
	mux.HandleFunc("GET /{$}", s.requireUser(s.handleGroupsPage))
	mux.HandleFunc("GET /chat/{folder...}", s.requireFolder(s.handleChatPage))

	// Private: JSON API.
	mux.HandleFunc("GET /api/groups", s.requireUser(s.handleAPIGroups))
	mux.HandleFunc("GET /api/groups/{rest...}", s.requireUser(s.routeAPIGroups))
	mux.HandleFunc("POST /api/groups/{rest...}", s.requireUser(s.routeAPIGroups))

	// Private: HTMX partials.
	mux.HandleFunc("GET /x/groups", s.requireUser(s.handleXGroups))
	mux.HandleFunc("GET /x/groups/{rest...}", s.requireUser(s.routeXGroups))

	return chanlib.LogMiddleware(mux)
}

// loadHMACSecret returns the proxyd-shared secret. If unset, a random
// value makes every sig-check fail closed (webd refuses unsigned identity).
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


// requireUser checks that X-User-Sub is present AND signed by proxyd.
// Without the signature check, a misconfigured compose (no proxyd in
// front) would let any client forge identity headers.
func (s *server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.VerifyUserSig(s.cfg.hmacSecret, r) {
			attemptedSub := r.Header.Get("X-User-Sub")
			slog.Warn("user sig verify failed", "path", r.URL.Path,
				"attempted_sub", attemptedSub,
				"remote", r.Header.Get("X-Forwarded-For"))
			for _, h := range []string{"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig"} {
				r.Header.Del(h)
			}
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func userGroups(r *http.Request) []string {
	var out []string
	if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
		_ = json.Unmarshal([]byte(hdr), &out)
	}
	return out
}

// userAllowedFolder: `**` = any; exact match; parent-prefix ("atlas"
// covers "atlas" and "atlas/...").
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

// splitFolderSuffix splits "atlas/content/topics" → ("atlas/content",
// "topics"). Bare "/topics" does NOT match — prevents folder="" from
// slipping past ACL via an empty-string grant.
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
