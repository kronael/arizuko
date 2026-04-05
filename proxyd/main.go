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

type vhosts struct {
	mu      sync.RWMutex
	entries map[string]string
	path    string
	mtime   time.Time
}

func newVhosts(p string) *vhosts {
	v := &vhosts{path: p, entries: map[string]string{}}
	v.load()
	return v
}

func (v *vhosts) load() {
	info, err := os.Stat(v.path)
	if os.IsNotExist(err) {
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

func (v *vhosts) match(host string) (string, bool) {
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

func (s *server) handler(cfg *core.Config) http.Handler {
	mux := http.NewServeMux()
	auth.RegisterRoutes(mux, s.st, cfg)
	mux.HandleFunc("/", s.route)
	return logging(mux)
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, code: 200}
		next.ServeHTTP(sw, r)
		slog.Info("request",
			"method", r.Method, "path", r.URL.Path,
			"status", sw.code, "dur", time.Since(start).String())
	})
}

func (s *server) route(w http.ResponseWriter, r *http.Request) {
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

	if strings.HasPrefix(r.URL.Path, "/dash/") {
		if s.dashProxy == nil {
			http.NotFound(w, r)
			return
		}
		s.requireAuth(s.dashProxy.ServeHTTP)(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/dav/") || r.URL.Path == "/dav" {
		if s.davProxy == nil {
			http.Error(w, "WebDAV not configured", http.StatusNotFound)
			return
		}
		s.requireAuth(s.davRoute)(w, r)
		return
	}

	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
		return
	}

	upstream := s.viteProxy
	if s.webdProxy != nil {
		upstream = s.webdProxy
	}

	if strings.HasPrefix(r.URL.Path, "/slink/") {
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !s.slinkRL.allow(remoteIP) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
		token := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/slink/"), "/", 2)[0]
		if token != "" && s.st != nil {
			if group, ok := s.st.GroupBySlinkToken(token); ok {
				r = r.Clone(r.Context())
				r.Header.Set("X-Folder", group.Folder)
				r.Header.Set("X-Group-Name", group.Name)
				r.Header.Set("X-Slink-Token", token)
			}
		}
		upstream.ServeHTTP(w, r)
		return
	}

	if r.URL.Path == "/" || r.URL.Path == "/pub" {
		http.Redirect(w, r, "/pub/", http.StatusFound)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/pub/") {
		upstream.ServeHTTP(w, r)
	} else {
		s.requireAuth(upstream.ServeHTTP)(w, r)
	}
}

func (s *server) davRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/dav"), "/")
	hdr := r.Header.Get("X-User-Groups")
	var gs []string
	if hdr != "" {
		if err := json.Unmarshal([]byte(hdr), &gs); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	if rest == "" {
		group := "root"
		if len(gs) > 0 {
			group = gs[0]
		}
		http.Redirect(w, r, "/dav/"+group+"/", http.StatusFound)
		return
	}

	if hdr == "" {
		s.davProxy.ServeHTTP(w, r)
		return
	}
	group := strings.SplitN(rest, "/", 2)[0]
	for _, f := range gs {
		if f == group || strings.HasPrefix(group, f+"/") {
			s.davProxy.ServeHTTP(w, r)
			return
		}
	}
	http.Error(w, "Forbidden", http.StatusForbidden)
}

func setUserHeaders(r *http.Request, sub, name string, groups *[]string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-User-Sub", sub)
	r2.Header.Set("X-User-Name", name)
	if groups != nil {
		if b, err := json.Marshal(groups); err == nil {
			r2.Header.Set("X-User-Groups", string(b))
		}
	}
	return r2
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// No auth secret configured = nobody can authenticate. Fail closed:
		// private routes are simply unreachable. /pub/* and /auth/* still
		// route normally (they don't go through requireAuth).
		if s.cfg.authSecret == "" {
			http.NotFound(w, r)
			return
		}
		secret := []byte(s.cfg.authSecret)

		if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
			claims, err := auth.VerifyJWT(secret, strings.TrimPrefix(hdr, "Bearer "))
			if err != nil {
				http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
				return
			}
			next(w, setUserHeaders(r, claims.Sub, claims.Name, claims.Groups))
			return
		}

		if s.st != nil {
			if cookie, err := r.Cookie("refresh_token"); err == nil {
				h := auth.HashToken(cookie.Value)
				if sess, ok := s.st.AuthSession(h); ok && time.Now().Before(sess.ExpiresAt) {
					if u, ok := s.st.AuthUserBySub(sess.UserSub); ok {
						next(w, setUserHeaders(r, u.Sub, u.Name, s.st.UserGroups(u.Sub)))
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

	slog.Info("proxyd starting",
		"port", cfg.port, "dash", cfg.dashAddr, "webd", cfg.webdAddr, "vite", cfg.viteAddr)

	srv := &http.Server{
		Addr:    cfg.port,
		Handler: s.handler(coreCfg),
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
