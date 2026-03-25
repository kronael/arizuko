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
	"path"
	"path/filepath"
	"strings"
	"sync"
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
	webdAddr   string
	viteAddr   string
	authSecret string
	webPublic  bool
}

func loadConfig() config {
	port := chanlib.EnvOr("WEB_PORT", "8095")
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	return config{
		port:       port,
		dashAddr:   chanlib.EnvOr("DASH_ADDR", "http://dashd:8091"),
		webdAddr:   chanlib.EnvOr("WEBD_ADDR", ""),
		viteAddr:   chanlib.EnvOr("VITE_ADDR", "http://localhost:8096"),
		authSecret: os.Getenv("AUTH_SECRET"),
		webPublic:  chanlib.EnvOr("WEB_PUBLIC", "false") == "true",
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

// vhosts manages hostname→world routing loaded from vhosts.json.
type vhosts struct {
	mu      sync.RWMutex
	entries map[string]string
	path    string
	mtime   time.Time
}

func newVhosts(p string) *vhosts { return &vhosts{path: p, entries: map[string]string{}} }

func (v *vhosts) load() {
	info, err := os.Stat(v.path)
	if os.IsNotExist(err) {
		slog.Debug("vhosts.json not found, skipping", "path", v.path)
		return
	}
	if err != nil {
		slog.Warn("vhosts stat failed", "err", err)
		return
	}
	if !info.ModTime().After(v.mtime) {
		return
	}
	raw, err := os.ReadFile(v.path)
	if err != nil {
		slog.Warn("vhosts read failed", "err", err)
		return
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		slog.Warn("vhosts parse failed", "err", err)
		return
	}
	v.mu.Lock()
	v.entries = m
	v.mtime = info.ModTime()
	v.mu.Unlock()
	slog.Info("vhosts loaded", "count", len(m))
}

// match returns the world folder for the given host, or ("", false).
func (v *vhosts) match(host string) (string, bool) {
	// strip port
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	v.mu.RLock()
	defer v.mu.RUnlock()
	if world, ok := v.entries[host]; ok {
		return world, true
	}
	for pattern, world := range v.entries {
		if ok, _ := path.Match(pattern, host); ok {
			return world, true
		}
	}
	return "", false
}

type server struct {
	cfg       config
	st        *store.Store
	dashProxy *httputil.ReverseProxy
	webdProxy *httputil.ReverseProxy
	viteProxy *httputil.ReverseProxy
	vh        *vhosts
	slinkRL   *rateLimiter
}

func newServer(cfg config, st *store.Store, vh *vhosts) *server {
	s := &server{
		cfg:       cfg,
		st:        st,
		dashProxy: proxy(cfg.dashAddr),
		viteProxy: proxy(cfg.viteAddr),
		vh:        vh,
		slinkRL:   newRateLimiter(10, time.Minute),
	}
	if cfg.webdAddr != "" {
		s.webdProxy = proxy(cfg.webdAddr)
	}
	return s
}

// rateLimiter is a simple sliding-window rate limiter keyed by IP.
type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string][]time.Time
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, buckets: make(map[string][]time.Time)}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)
	hits := rl.buckets[key]
	n := 0
	for _, t := range hits {
		if t.After(cutoff) {
			hits[n] = t
			n++
		}
	}
	hits = hits[:n]
	if len(hits) >= rl.limit {
		rl.buckets[key] = hits
		return false
	}
	rl.buckets[key] = append(hits, now)
	return true
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
	// 1. vhosts hostname match → 301 redirect to /<world><path>
	if world, ok := s.vh.match(r.Host); ok {
		rawPath := r.URL.Path
		if strings.Contains(rawPath, "..") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		target := path.Clean("/" + world + "/" + strings.TrimPrefix(rawPath, "/"))
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, target, http.StatusMovedPermanently)
		return
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

	// 4. /slink/* — rate-limited; resolve token and inject group headers
	if strings.HasPrefix(r.URL.Path, "/slink/") {
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !s.slinkRL.allow(remoteIP) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		upstream := s.webdProxy
		if upstream == nil {
			upstream = s.viteProxy
		}
		// Extract token: first path segment after /slink/
		rest := strings.TrimPrefix(r.URL.Path, "/slink/")
		token := strings.SplitN(rest, "/", 2)[0]
		if token != "" && s.st != nil {
			if group, ok := s.st.GroupBySlinkToken(token); ok {
				r2 := r.Clone(r.Context())
				r2.Header.Set("X-Folder", group.Folder)
				r2.Header.Set("X-Group-Name", group.Name)
				r2.Header.Set("X-Slink-Token", token)
				upstream.ServeHTTP(w, r2)
				return
			}
		}
		upstream.ServeHTTP(w, r)
		return
	}

	// 5. /* — webd (if configured) or Vite, auth-gated unless WEB_PUBLIC=true
	upstream := s.viteProxy
	if s.webdProxy != nil {
		upstream = s.webdProxy
	}
	if s.cfg.webPublic {
		upstream.ServeHTTP(w, r)
	} else {
		s.requireAuth(upstream.ServeHTTP)(w, r)
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
		claims, err := auth.VerifyJWT(secret, token)
		if err != nil {
			http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
			return
		}
		r2 := r.Clone(r.Context())
		r2.Header.Set("X-User-Sub", claims.Sub)
		r2.Header.Set("X-User-Name", claims.Name)
		if claims.Groups != nil {
			if b, err := json.Marshal(claims.Groups); err == nil {
				r2.Header.Set("X-User-Groups", string(b))
			}
		}
		next(w, r2)
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

	vh := newVhosts(filepath.Join(coreCfg.WebDir, "vhosts.json"))
	vh.load()

	s := newServer(cfg, st, vh)

	go func() {
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for range t.C {
			vh.load()
		}
	}()

	slog.Info("proxyd starting", "port", cfg.port, "dash", cfg.dashAddr, "webd", cfg.webdAddr, "vite", cfg.viteAddr)

	srv := &http.Server{
		Addr:    cfg.port,
		Handler: s.handler(st, coreCfg),
	}

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("proxyd failed", "err", err)
		os.Exit(1)
	}
}
