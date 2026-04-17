package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

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

	return loggingMiddleware(mux)
}

// loadHMACSecret returns the shared secret used to verify proxyd-signed
// headers. Generated at startup if unset — but in that case proxyd and
// webd won't share a value, so every verifyUserSig will fail closed (the
// intended outcome: webd refuses unsigned identity headers).
func loadHMACSecret() string {
	if v := os.Getenv("PROXYD_HMAC_SECRET"); v != "" {
		return v
	}
	var b [32]byte
	if _, err := rand.Read(b[:]); err == nil {
		slog.Warn("PROXYD_HMAC_SECRET unset in webd — all signed-header verification will fail until set on both webd and proxyd")
		return hex.EncodeToString(b[:])
	}
	return ""
}

// verifyUserSig returns true if X-User-Sig matches HMAC(sub|name|groupsJSON).
// Missing header, empty secret, or mismatched signature → false.
func verifyUserSig(secret string, r *http.Request) bool {
	if secret == "" {
		return false
	}
	sub := r.Header.Get("X-User-Sub")
	if sub == "" {
		return false
	}
	sig := r.Header.Get("X-User-Sig")
	if sig == "" {
		return false
	}
	msg := "user:" + sub + "|" + r.Header.Get("X-User-Name") + "|" + r.Header.Get("X-User-Groups")
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	want := hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sig))
}

// verifySlinkSig returns true if X-Slink-Sig matches HMAC(token|folder).
func verifySlinkSig(secret string, r *http.Request) bool {
	if secret == "" {
		return false
	}
	token := r.Header.Get("X-Slink-Token")
	folder := r.Header.Get("X-Folder")
	sig := r.Header.Get("X-Slink-Sig")
	if token == "" || folder == "" || sig == "" {
		return false
	}
	msg := "slink:" + token + "|" + folder
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	want := hex.EncodeToString(h.Sum(nil))
	return hmac.Equal([]byte(want), []byte(sig))
}

// requireUser checks that X-User-Sub is present AND signed by proxyd.
// Without the signature check, any client reaching webd directly (e.g.
// via a misconfigured compose, leaked internal port, or absence of
// proxyd) could forge identity headers and bypass auth entirely.
func (s *server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyUserSig(s.cfg.hmacSecret, r) {
			// Drop unsigned identity headers before any further handling
			// so downstream code can't accidentally trust them.
			for _, h := range []string{"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig"} {
				r.Header.Del(h)
			}
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// userGroups parses X-User-Groups (JSON string array). `**` means any.
func userGroups(r *http.Request) []string {
	var out []string
	if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
		_ = json.Unmarshal([]byte(hdr), &out)
	}
	return out
}

// userAllowedFolder returns true if the user's grants cover folder.
// `**` = universal; exact match; parent-prefix (grant "atlas" covers
// "atlas" and "atlas/..."). Operator is implicit via `**`.
func userAllowedFolder(groups []string, folder string) bool {
	for _, f := range groups {
		if f == "**" || f == folder || strings.HasPrefix(folder, f+"/") {
			return true
		}
	}
	return false
}

// requireFolder wraps requireUser and enforces folder-ACL for endpoints
// that carry folder in the URL path.
func (s *server) requireFolder(next http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if !userAllowedFolder(userGroups(r), folderParam(r)) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

// splitFolderSuffix splits "atlas/content/topics" → ("atlas/content",
// "topics"). The suffix must be a distinct preceding path segment:
// "topics" alone or a folder literally named "topics" with nothing in
// front will NOT mis-route to an empty folder. Previously a request to
// "/topics" (folder literally named "topics") was split into
// folder="", which let callers with a grant on "" slip through.
func splitFolderSuffix(rest string) (string, string) {
	for _, suffix := range []string{"/topics", "/messages", "/typing"} {
		if !strings.HasSuffix(rest, suffix) {
			continue
		}
		folder := rest[:len(rest)-len(suffix)]
		if folder == "" {
			// "/topics" is not a <folder>/topics; no match.
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

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		slog.Info("request", "method", r.Method, "path", r.URL.Path,
			"status", sw.code, "dur", time.Since(start).String())
	})
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.code = code
	sw.ResponseWriter.WriteHeader(code)
}

// Flush passes through to the underlying ResponseWriter so SSE handlers
// keep streaming through the logging middleware wrapper.
func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{
		"status": "ok", "name": "web", "jid_prefixes": []string{"web:"},
	})
}
