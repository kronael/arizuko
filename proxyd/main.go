package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/onvos/arizuko/auth"
	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

type config struct {
	port       string
	dashAddr   string
	viteAddr   string
	authSecret string
	webPublic  bool
	redirects  map[string]string
}

func loadConfig() config {
	port := chanlib.EnvOr("WEB_PORT", "8095")
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}

	redirects := map[string]string{}
	if raw := os.Getenv("WEB_REDIRECTS"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &redirects); err != nil {
			slog.Warn("WEB_REDIRECTS parse failed", "err", err)
		}
	}

	return config{
		port:       port,
		dashAddr:   chanlib.EnvOr("DASH_ADDR", "http://dashd:8091"),
		viteAddr:   chanlib.EnvOr("VITE_ADDR", "http://localhost:8096"),
		authSecret: os.Getenv("AUTH_SECRET"),
		webPublic:  chanlib.EnvOr("WEB_PUBLIC", "false") == "true",
		redirects:  redirects,
	}
}

func proxy(target string) *httputil.ReverseProxy {
	u, err := url.Parse(target)
	if err != nil {
		slog.Error("invalid proxy target", "target", target, "err", err)
		os.Exit(1)
	}
	return httputil.NewSingleHostReverseProxy(u)
}

type server struct {
	cfg       config
	dashProxy *httputil.ReverseProxy
	viteProxy *httputil.ReverseProxy
	redirects map[string]*httputil.ReverseProxy
}

func newServer(cfg config) *server {
	s := &server{
		cfg:       cfg,
		dashProxy: proxy(cfg.dashAddr),
		viteProxy: proxy(cfg.viteAddr),
		redirects: make(map[string]*httputil.ReverseProxy, len(cfg.redirects)),
	}
	for prefix, upstream := range cfg.redirects {
		s.redirects[prefix] = proxy(upstream)
	}
	return s
}

type statusWriter struct {
	http.ResponseWriter
	code int
}

func (sw *statusWriter) WriteHeader(code int) {
	sw.code = code
	sw.ResponseWriter.WriteHeader(code)
}

func (sw *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := sw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

func (s *server) handler(st *store.Store, cfg *core.Config) http.Handler {
	mux := http.NewServeMux()
	auth.RegisterRoutes(mux, st, cfg)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		s.route(sw, r)
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.code,
			"dur", time.Since(start).String(),
		)
	})

	return mux
}

func (s *server) route(w http.ResponseWriter, r *http.Request) {
	// 1. WEB_REDIRECTS prefix match
	for prefix, rp := range s.redirects {
		if strings.HasPrefix(r.URL.Path, prefix) {
			rp.ServeHTTP(w, r)
			return
		}
	}

	// 2. /dash/ — always auth-gated
	if strings.HasPrefix(r.URL.Path, "/dash/") {
		s.requireAuth(s.dashProxy.ServeHTTP)(w, r)
		return
	}

	// 3. /health — public
	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
		return
	}

	// 4. /* — Vite, auth-gated unless WEB_PUBLIC=true
	if s.cfg.webPublic {
		s.viteProxy.ServeHTTP(w, r)
	} else {
		s.requireAuth(s.viteProxy.ServeHTTP)(w, r)
	}
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.authSecret == "" {
			next(w, r)
			return
		}
		secret := []byte(s.cfg.authSecret)

		token := ""
		hdr := r.Header.Get("Authorization")
		if strings.HasPrefix(hdr, "Bearer ") {
			tok := strings.TrimPrefix(hdr, "Bearer ")
			if tok == s.cfg.authSecret {
				next(w, r)
				return
			}
			token = tok
		}
		if token == "" {
			if cookie, err := r.Cookie("session"); err == nil {
				token = cookie.Value
			}
		}
		if token == "" {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		if _, err := auth.VerifyJWT(secret, token); err != nil {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		next(w, r)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	cfg := loadConfig()

	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	st, err := store.Open(coreCfg.StoreDir)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	s := newServer(cfg)

	slog.Info("proxyd starting", "port", cfg.port, "dash", cfg.dashAddr, "vite", cfg.viteAddr)

	srv := &http.Server{
		Addr:    cfg.port,
		Handler: s.handler(st, coreCfg),
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("proxyd failed", "err", err)
		os.Exit(1)
	}
}
