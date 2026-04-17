package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	port           string
	dashAddr       string
	webdAddr       string
	davAddr        string
	viteAddr       string
	onbodAddr      string
	authSecret     string
	hmacSecret     string
	trustedProxies []*net.IPNet
}

func loadConfig() config {
	port := chanlib.EnvOr("PROXYD_LISTEN", "8080")
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	hmacSecret := os.Getenv("PROXYD_HMAC_SECRET")
	if hmacSecret == "" {
		var b [32]byte
		if _, err := rand.Read(b[:]); err == nil {
			hmacSecret = hex.EncodeToString(b[:])
			slog.Warn("PROXYD_HMAC_SECRET unset; generated ephemeral secret — webd will reject header signatures unless both share the same env value")
		}
	}
	return config{
		port:           port,
		dashAddr:       chanlib.EnvOr("DASH_ADDR", "http://dashd:8080"),
		webdAddr:       chanlib.EnvOr("WEBD_ADDR", "http://webd:8080"),
		davAddr:        chanlib.EnvOr("DAV_ADDR", ""),
		viteAddr:       chanlib.EnvOr("VITE_ADDR", "http://vited:8080"),
		onbodAddr:      chanlib.EnvOr("ONBOD_ADDR", ""),
		hmacSecret:     hmacSecret,
		trustedProxies: parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
	}
}

// parseTrustedProxies parses comma-separated CIDRs; bare IP → /32 or /128.
// Empty = no client trusted; XFF is always replaced with the connection peer.
func parseTrustedProxies(s string) []*net.IPNet {
	var out []*net.IPNet
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			if strings.Contains(part, ":") {
				part += "/128"
			} else {
				part += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(part); err == nil {
			out = append(out, n)
		} else {
			slog.Warn("invalid TRUSTED_PROXIES entry", "entry", part, "err", err)
		}
	}
	return out
}

// clientHeaderNames are proxyd-owned; stripped on entry and repopulated
// only after auth or slink-token resolution.
var clientHeaderNames = []string{
	"X-User-Sub",
	"X-User-Name",
	"X-User-Groups",
	"X-User-Sig",
	"X-Folder",
	"X-Group-Name",
	"X-Slink-Token",
	"X-Slink-Sig",
}

func stripClientHeaders(r *http.Request) {
	for _, h := range clientHeaderNames {
		r.Header.Del(h)
	}
}

func hmacSign(secret, msg string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

func userSigMessage(sub, name, groupsJSON string) string {
	return "user:" + sub + "|" + name + "|" + groupsJSON
}

func slinkSigMessage(token, folder string) string {
	return "slink:" + token + "|" + folder
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
	cfg        config
	st         *store.Store
	dashProxy  *httputil.ReverseProxy
	webdProxy  *httputil.ReverseProxy
	davProxy   *httputil.ReverseProxy
	viteProxy  *httputil.ReverseProxy
	onbodProxy *httputil.ReverseProxy
	vh         *vhosts
	slinkRL    *rateLimiter
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
	if cfg.onbodAddr != "" {
		s.onbodProxy = proxy(cfg.onbodAddr)
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

	// Sweep stale buckets to bound map size under distinct-IP flood.
	for k, hits := range rl.buckets {
		if len(hits) == 0 || hits[len(hits)-1].Before(cutoff) {
			delete(rl.buckets, k)
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
		return nil, nil, errors.New("ResponseWriter does not implement http.Hijacker")
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

func (s *server) fixForwardedFor(r *http.Request) {
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	peerIP := net.ParseIP(peer)
	trusted := false
	if peerIP != nil {
		for _, n := range s.cfg.trustedProxies {
			if n.Contains(peerIP) {
				trusted = true
				break
			}
		}
	}
	if trusted {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			left := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
			if left != "" {
				r.Header.Set("X-Forwarded-For", left)
				return
			}
		}
	}
	if peer == "" {
		r.Header.Del("X-Forwarded-For")
		return
	}
	r.Header.Set("X-Forwarded-For", peer)
}

func (s *server) route(w http.ResponseWriter, r *http.Request) {
	stripClientHeaders(r)
	s.fixForwardedFor(r)

	if world, ok := s.vh.match(r.Host); ok {
		rawPath := r.URL.Path
		lowRaw := strings.ToLower(r.URL.RawPath)
		if strings.Contains(rawPath, "..") ||
			strings.Contains(lowRaw, "%2e%2e") ||
			strings.Contains(lowRaw, "%2f") {
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
		var token string
		if r.URL.Path == "/slink/stream" {
			token = r.URL.Query().Get("token")
		} else {
			token = strings.SplitN(strings.TrimPrefix(r.URL.Path, "/slink/"), "/", 2)[0]
		}
		if token != "" && s.st != nil {
			if group, ok := s.st.GroupBySlinkToken(token); ok {
				r = r.Clone(r.Context())
				r.Header.Set("X-Folder", group.Folder)
				r.Header.Set("X-Group-Name", group.Name)
				r.Header.Set("X-Slink-Token", token)
				r.Header.Set("X-Slink-Sig",
					hmacSign(s.cfg.hmacSecret, slinkSigMessage(token, group.Folder)))
			}
		}
		// Attach signed user identity when also logged in so webd can also
		// accept via folder ACL.
		s.optionalAuth(upstream.ServeHTTP)(w, r)
		return
	}

	if r.URL.Path == "/onboard" || strings.HasPrefix(r.URL.Path, "/onboard/") {
		if s.onbodProxy == nil {
			http.NotFound(w, r)
			return
		}
		s.optionalAuth(s.onbodProxy.ServeHTTP)(w, r)
		return
	}

	if r.URL.Path == "/" || r.URL.Path == "/pub" {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			upstream.ServeHTTP(w, r)
			return
		}
		http.Redirect(w, r, "/pub/", http.StatusFound)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/pub/") {
		s.viteProxy.ServeHTTP(w, r)
		return
	}
	for _, p := range []string{"/chat/", "/api/", "/x/", "/static/", "/auth/", "/mcp"} {
		if strings.HasPrefix(r.URL.Path, p) {
			s.requireAuth(upstream.ServeHTTP)(w, r)
			return
		}
	}
	http.Redirect(w, r, "/pub"+r.URL.Path, http.StatusFound)
}

func (s *server) davRoute(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "..") ||
		strings.Contains(strings.ToLower(r.URL.RawPath), "%2e%2e") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/dav"), "/")
	var gs []string
	if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
		if err := json.Unmarshal([]byte(hdr), &gs); err != nil {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
	}

	if rest == "" {
		group := "root"
		for _, g := range gs {
			if g != "**" {
				group = g
				break
			}
		}
		http.Redirect(w, r, "/dav/"+group+"/", http.StatusFound)
		return
	}

	group := strings.SplitN(rest, "/", 2)[0]
	if auth.MatchGroups(gs, group) {
		s.davProxy.ServeHTTP(w, r)
		return
	}
	http.Error(w, "Forbidden", http.StatusForbidden)
}

func (s *server) setUserHeaders(r *http.Request, sub, name string, groups []string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-User-Sub", sub)
	r2.Header.Set("X-User-Name", name)
	groupsJSON := "null"
	if b, err := json.Marshal(groups); err == nil {
		groupsJSON = string(b)
	}
	r2.Header.Set("X-User-Groups", groupsJSON)
	if s.cfg.hmacSecret != "" {
		r2.Header.Set("X-User-Sig",
			hmacSign(s.cfg.hmacSecret, userSigMessage(sub, name, groupsJSON)))
	}
	return r2
}

func (s *server) authByCookie(r *http.Request) *http.Request {
	if s.st == nil {
		return nil
	}
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		return nil
	}
	sess, ok := s.st.AuthSession(auth.HashToken(cookie.Value))
	if !ok || !time.Now().Before(sess.ExpiresAt) {
		return nil
	}
	u, ok := s.st.AuthUserBySub(sess.UserSub)
	if !ok {
		return nil
	}
	return s.setUserHeaders(r, u.Sub, u.Name, s.st.UserGroups(u.Sub))
}

// tryAuth returns an identity-stamped request if the caller has a valid
// Bearer JWT or refresh-token cookie; otherwise the original request.
func (s *server) tryAuth(r *http.Request) *http.Request {
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
		if c, err := auth.VerifyJWT([]byte(s.cfg.authSecret), strings.TrimPrefix(hdr, "Bearer ")); err == nil {
			return s.setUserHeaders(r, c.Sub, c.Name, c.Groups)
		}
	}
	if a := s.authByCookie(r); a != nil {
		return a
	}
	return nil
}

func (s *server) optionalAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if a := s.tryAuth(r); a != nil {
			r = a
		}
		next(w, r)
	}
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.authSecret == "" {
			http.NotFound(w, r)
			return
		}
		if a := s.tryAuth(r); a != nil {
			next(w, a)
			return
		}
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	cfg := loadConfig()
	cfg.authSecret = coreCfg.AuthSecret

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
