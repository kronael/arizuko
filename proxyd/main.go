package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
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
	davAddr    string
	viteAddr   string
	authSecret string
}

func loadConfig() config {
	port := chanlib.EnvOr("WEB_PORT", "8095")
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	return config{
		port:       port,
		dashAddr:   chanlib.EnvOr("DASH_ADDR", ""),
		webdAddr:   chanlib.EnvOr("WEBD_ADDR", ""),
		davAddr:    chanlib.EnvOr("DAV_ADDR", ""),
		viteAddr:   chanlib.EnvOr("VITE_ADDR", "http://localhost:8096"),
		authSecret: os.Getenv("AUTH_SECRET"),
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

func davProxy(target string) *httputil.ReverseProxy {
	p := proxy(target)
	orig := p.Director
	p.Director = func(r *http.Request) {
		orig(r)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/dav")
		if r.URL.Path == "" {
			r.URL.Path = "/"
		}
		r.URL.RawPath = strings.TrimPrefix(r.URL.RawPath, "/dav")
	}
	return p
}

// vhosts manages hostname→world routing loaded from vhosts.json.
type vhosts struct {
	mu      sync.RWMutex
	entries map[string]string
	path    string
	mtime   time.Time
}

func newVhosts(p string) *vhosts {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		os.WriteFile(p, []byte("{}"), 0o644)
	}
	v := &vhosts{path: p, entries: map[string]string{}}
	v.load()
	return v
}

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
	davProxy  *httputil.ReverseProxy
	viteProxy *httputil.ReverseProxy
	vh        *vhosts
	slinkRL   *rateLimiter
}

func newServer(cfg config, st *store.Store, vh *vhosts) *server {
	s := &server{
		cfg:       cfg,
		st:        st,
		viteProxy: proxy(cfg.viteAddr),
		vh:        vh,
		slinkRL:   newRateLimiter(10, time.Minute),
	}
	if cfg.dashAddr != "" {
		s.dashProxy = proxy(cfg.dashAddr)
	}
	if cfg.webdAddr != "" {
		s.webdProxy = proxy(cfg.webdAddr)
	}
	if cfg.davAddr != "" {
		s.davProxy = davProxy(cfg.davAddr)
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

	if len(rl.buckets) > 10000 {
		for k, hits := range rl.buckets {
			if len(hits) == 0 || hits[len(hits)-1].Before(cutoff) {
				delete(rl.buckets, k)
			}
		}
	}

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

func (sw *statusWriter) Flush() {
	if f, ok := sw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
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
	// 1. vhosts hostname match → rewrite path to /<world><path> and serve via vite
	if world, ok := s.vh.match(r.Host); ok {
		rawPath := r.URL.Path
		if strings.Contains(rawPath, "..") {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		r.URL.Path = path.Clean("/" + world + "/" + strings.TrimPrefix(rawPath, "/"))
		r.URL.RawPath = ""
		if s.viteProxy != nil {
			s.viteProxy.ServeHTTP(w, r)
		} else {
			http.NotFound(w, r)
		}
		return
	}

	// 2. /dash/ — auth-gated, proxied to dashd (not available without dashd)
	if strings.HasPrefix(r.URL.Path, "/dash/") {
		if s.dashProxy == nil {
			http.NotFound(w, r)
			return
		}
		s.requireAuth(s.dashProxy.ServeHTTP)(w, r)
		return
	}

	// 3. /dav/ — WebDAV via dufs, auth-gated with per-group ACL
	if strings.HasPrefix(r.URL.Path, "/dav/") || r.URL.Path == "/dav" {
		if s.davProxy == nil {
			http.Error(w, "WebDAV not configured", http.StatusNotFound)
			return
		}
		s.requireAuth(s.davRoute)(w, r)
		return
	}

	// 4. /health — public
	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
		return
	}

	// 5. /slink/* — rate-limited; resolve token and inject group headers
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

	// 6. /pub/ — explicitly public, no auth
	// 7. /* — everything else auth-gated
	upstream := s.viteProxy
	if s.webdProxy != nil {
		upstream = s.webdProxy
	}
	if strings.HasPrefix(r.URL.Path, "/pub/") {
		upstream.ServeHTTP(w, r)
	} else {
		s.requireAuth(upstream.ServeHTTP)(w, r)
	}
}

// davRoute handles /dav requests with per-group ACL.
// Called after requireAuth — X-User-Sub and optionally X-User-Groups are set.
func (s *server) davRoute(w http.ResponseWriter, r *http.Request) {
	// /dav or /dav/ with no group → redirect to first allowed group
	rest := strings.TrimPrefix(r.URL.Path, "/dav")
	rest = strings.TrimPrefix(rest, "/")
	if rest == "" {
		group := "root"
		groupsHdr := r.Header.Get("X-User-Groups")
		if groupsHdr != "" {
			var allowed []string
			if err := json.Unmarshal([]byte(groupsHdr), &allowed); err == nil && len(allowed) > 0 {
				group = allowed[0]
			}
		}
		http.Redirect(w, r, "/dav/"+group+"/", http.StatusFound)
		return
	}

	// Extract group: first path segment after /dav/
	group := strings.SplitN(rest, "/", 2)[0]

	// Check per-group ACL via X-User-Groups (set by requireAuth)
	groupsHdr := r.Header.Get("X-User-Groups")
	if groupsHdr != "" {
		var allowed []string
		if err := json.Unmarshal([]byte(groupsHdr), &allowed); err != nil {
			// Fail closed on parse error
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		ok := false
		for _, f := range allowed {
			if f == group || strings.HasPrefix(group, f+"/") {
				ok = true
				break
			}
		}
		if !ok {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}
	// groupsHdr empty = operator (unrestricted)

	s.davProxy.ServeHTTP(w, r)
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.authSecret == "" {
			next(w, r)
			return
		}
		secret := []byte(s.cfg.authSecret)

		// Check Authorization: Bearer <jwt>
		hdr := r.Header.Get("Authorization")
		if strings.HasPrefix(hdr, "Bearer ") {
			tok := strings.TrimPrefix(hdr, "Bearer ")
			claims, err := auth.VerifyJWT(secret, tok)
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
			return
		}

		// Fall back to refresh cookie for browser navigation (no JS Bearer header)
		if s.st != nil {
			if cookie, err := r.Cookie("refresh_token"); err == nil {
				h := auth.HashToken(cookie.Value)
				if sess, ok := s.st.AuthSession(h); ok && time.Now().Before(sess.ExpiresAt) {
					if u, ok := s.st.AuthUserBySub(sess.UserSub); ok {
						r2 := r.Clone(r.Context())
						r2.Header.Set("X-User-Sub", u.Sub)
						r2.Header.Set("X-User-Name", u.Name)
						if groups := s.st.UserGroups(u.Sub); groups != nil {
							if b, err := json.Marshal(groups); err == nil {
								r2.Header.Set("X-User-Groups", string(b))
							}
						}
						next(w, r2)
						return
					}
				}
			}
		}

		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
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

	os.MkdirAll(coreCfg.WebDir, 0o755)
	vh := newVhosts(filepath.Join(coreCfg.WebDir, "vhosts.json"))

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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("proxyd stopping")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("proxyd failed", "err", err)
		os.Exit(1)
	}
}
