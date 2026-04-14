package main

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/store"
)

//go:embed static
var staticFS embed.FS

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
	mux.HandleFunc("POST /slink/{token}", s.handleSlinkPost)
	mux.HandleFunc("GET /slink/stream", s.handleSlinkStream)

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

// requireUser checks that X-User-Sub is present (injected by proxyd after auth).
func (s *server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User-Sub") == "" {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

// requireFolder wraps requireUser and additionally enforces X-User-Groups
// access for folder-specific endpoints. Groups header absent = operator access.
func (s *server) requireFolder(next http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		groupsHdr := r.Header.Get("X-User-Groups")
		if groupsHdr == "" {
			next(w, r)
			return
		}
		var allowed []string
		if err := json.Unmarshal([]byte(groupsHdr), &allowed); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		folder := folderParam(r)
		for _, f := range allowed {
			if f == folder || strings.HasPrefix(folder, f+"/") {
				next(w, r)
				return
			}
		}
		http.Error(w, "Forbidden", http.StatusForbidden)
	})
}

// splitFolderSuffix splits "atlas/content/topics" → ("atlas/content", "topics").
func splitFolderSuffix(rest string) (string, string) {
	for _, suffix := range []string{"/topics", "/messages", "/typing"} {
		if strings.HasSuffix(rest, suffix) {
			return rest[:len(rest)-len(suffix)], suffix[1:]
		}
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

func (s *server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	chanlib.WriteJSON(w, map[string]any{
		"status": "ok", "name": "web", "jid_prefixes": []string{"web:"},
	})
}
