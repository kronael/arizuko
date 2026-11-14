package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/obs"
	"github.com/kronael/arizuko/resreg"
	_ "github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

type config struct {
	port           string
	webDir         string
	pubRedirectURL string
	authSecret     string
	authdURL       string
	trustedProxies []*net.IPNet
	hostingDomain  string
	vhostAliases   map[string]string
}

func loadConfig() config {
	port := core.EnvOr("PROXYD_LISTEN", "8080")
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	return config{
		port:           port,
		pubRedirectURL: strings.TrimRight(core.EnvOr("PUB_REDIRECT_URL", ""), "/"),
		authdURL:       strings.TrimRight(os.Getenv("AUTHD_URL"), "/"),
		trustedProxies: parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		hostingDomain:  strings.ToLower(strings.TrimSuffix(core.EnvOr("HOSTING_DOMAIN", ""), ".")),
		vhostAliases:   groupfolder.ParseVhostAliases(os.Getenv("WEB_VHOST_ALIASES")),
	}
}

func parseTrustedProxies(s string) []*net.IPNet {
	var out []*net.IPNet
	for part := range strings.SplitSeq(s, ",") {
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

func stripClientHeaders(r *http.Request) {
	for _, h := range []string{
		"X-User-Sub", "X-User-Name", "X-User-Groups", "X-User-Sig",
		"X-Folder", "X-Group-Name", "X-Chat-Token", "X-Chat-Sig",
	} {
		r.Header.Del(h)
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
	cfg         config
	stRoutd     *store.Store
	rr          *routesResource
	files       http.Handler
	authdProxy  *httputil.ReverseProxy
	ks          *auth.KeySet
	svc         *auth.TokenSource
	pubRedir    *pubRedirect
	warnedEdges sync.Map
}

func (s *server) untrustedEdgeOnce(peer string) {
	if _, seen := s.warnedEdges.LoadOrStore(peer, struct{}{}); seen {
		return
	}
	slog.Warn("untrusted edge proxy: X-Forwarded-For discarded, client identity is the hop",
		"peer", peer, "fix", "add "+peer+" to TRUSTED_PROXIES")
}

func (s *server) routeSnapshot() ([]Route, map[string]*httputil.ReverseProxy, error) {
	if s.rr == nil {
		return nil, nil, nil
	}
	return s.rr.snapshot()
}

func (s *server) routes() ([]Route, error) {
	r, _, err := s.routeSnapshot()
	return r, err
}

func (s *server) webRoutes() ([]store.WebRoute, error) {
	if s.rr == nil {
		return nil, nil
	}
	return s.rr.webSnapshot()
}

func matchWebRoute(routes []store.WebRoute, urlPath string) (store.WebRoute, bool) {
	var best store.WebRoute
	found := false
	for _, wr := range routes {
		if strings.HasPrefix(urlPath, wr.PathPrefix) &&
			(!found || len(wr.PathPrefix) > len(best.PathPrefix)) {
			best = wr
			found = true
		}
	}
	return best, found
}

type pubRedirect struct {
	url    string
	ttl    time.Duration
	probe  func(url string) bool
	mu     sync.Mutex
	ok     bool
	expiry time.Time
}

func newPubRedirect(url string) *pubRedirect {
	if url == "" {
		return nil
	}
	return &pubRedirect{url: url, ttl: 30 * time.Second, probe: defaultProbe}
}

func defaultProbe(url string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode < 500
}

func (p *pubRedirect) reachable() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Now().Before(p.expiry) {
		return p.ok
	}
	p.ok = p.probe(p.url)
	p.expiry = time.Now().Add(p.ttl)
	return p.ok
}

func newServer(cfg config, stRoutd *store.Store, ks *auth.KeySet, svc *auth.TokenSource) *server {
	routes := loadInitialRoutes(stRoutd)
	rr := newRoutesResource(stRoutd, routes)
	var ap *httputil.ReverseProxy
	if cfg.authdURL != "" {
		ap = proxy(cfg.authdURL)
	}
	return &server{
		cfg:        cfg,
		stRoutd:    stRoutd,
		rr:         rr,
		files:      staticFiles(cfg.webDir),
		authdProxy: ap,
		ks:         ks,
		svc:        svc,
		pubRedir:   newPubRedirect(cfg.pubRedirectURL),
	}
}

func loadInitialRoutes(st *store.Store) []Route {
	if st == nil {
		return nil
	}
	stored, err := st.AllProxydRoutes()
	if err != nil {
		slog.Error("read proxyd_routes", "err", err)
		os.Exit(1)
	}
	routes := make([]Route, 0, len(stored))
	for _, r := range stored {
		routes = append(routes, fromStoreRoute(r))
	}
	for _, r := range routes {
		if r.RedirectTo != "" {
			continue
		}
		if p := buildRouteProxy(r); p == nil {
			slog.Error("invalid route backend; refusing to boot", "path", r.Path, "backend", r.Backend)
			os.Exit(1)
		}
	}
	slog.Info("proxyd routes loaded from db", "count", len(routes))
	return routes
}

func buildRouteProxy(r Route) *httputil.ReverseProxy {
	u, err := url.Parse(r.Backend)
	if err != nil {
		slog.Error("invalid route backend", "path", r.Path, "backend", r.Backend, "err", err)
		return nil
	}
	stripPrefix := r.StripPrefix
	prefix := strings.TrimSuffix(r.Path, "/")
	preserveKeys := append([]string(nil), r.PreserveHeaders...)
	backendHost := u.Host

	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		saved := map[string]string{}
		for _, k := range preserveKeys {
			if v := pr.In.Header.Get(k); v != "" {
				saved[k] = v
			}
		}
		pr.SetURL(u)
		pr.SetXForwarded()

		pr.Out.Host = backendHost
		if stripPrefix && prefix != "" {
			pr.Out.URL.Path = strings.TrimPrefix(pr.Out.URL.Path, prefix)
			if pr.Out.URL.Path == "" {
				pr.Out.URL.Path = "/"
			}
			pr.Out.URL.RawPath = strings.TrimPrefix(pr.Out.URL.RawPath, prefix)
		}
		for k, v := range saved {
			pr.Out.Header.Set(k, v)
		}
	}}
	return rp
}

func (s *server) mux() *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("/auth/forward", s.handleForwardAuth)
	mux.HandleFunc("/auth/", s.handleAuth)
	resreg.RegisterREST(mux, routesResourceDecl(s.rr), callerFromHTTP(s.ks))
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("proxyd", mux))

	mux.HandleFunc("GET /dash/proxyd/", s.requireAuth(s.handleDash))
	mux.HandleFunc("POST /dash/proxyd/", s.requireAuth(s.handleDashRouteCreate))
	mux.HandleFunc("POST /dash/proxyd/delete", s.requireAuth(s.handleDashRouteDelete))
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}
	mux.HandleFunc("/", s.route)
	return mux
}

func (s *server) handler(aud *audit.Audit) http.Handler {
	return obs.HTTPMiddleware("proxyd")(logging(s.mux(), aud))
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if left := strings.TrimSpace(strings.SplitN(xff, ",", 2)[0]); left != "" {
			return left
		}
	}
	peer, _, _ := net.SplitHostPort(r.RemoteAddr)
	return peer
}

func logging(next http.Handler, aud *audit.Audit) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &chanlib.StatusWriter{ResponseWriter: w, Code: 200}
		next.ServeHTTP(sw, r)
		peer := clientIP(r)
		slog.Info("request",
			"method", r.Method, "path", r.URL.Path,
			"status", sw.Code, "dur", time.Since(start).String(),
			"actor_sub", r.Header.Get("X-User-Sub"),
			"remote", peer, "host", r.Host)
		if aud != nil && r.URL.Path != "/health" {
			aud.EmitWeb(audit.WebEvent{
				TS:        start.UTC().Format(time.RFC3339Nano),
				Method:    r.Method,
				Path:      r.URL.Path,
				Status:    sw.Code,
				LatencyMS: time.Since(start).Milliseconds(),
				ActorSub:  r.Header.Get("X-User-Sub"),
				IP:        peer,
			})
		}
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
	} else if r.Header.Get("X-Forwarded-For") != "" {

		s.untrustedEdgeOnce(peer)
	}
	if peer == "" {
		r.Header.Del("X-Forwarded-For")
		return
	}
	r.Header.Set("X-Forwarded-For", peer)
}

func (s *server) worldForHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if w, ok := s.cfg.vhostAliases[host]; ok {
		return w
	}
	dom := s.cfg.hostingDomain
	if dom == "" || !strings.HasSuffix(host, "."+dom) {
		return ""
	}
	label := host[:len(host)-len(dom)-1]
	if label == "" || strings.Contains(label, ".") {
		return ""
	}
	return label
}

func (s *server) vhostRedirect(w http.ResponseWriter, r *http.Request, world string) {
	rawPath := r.URL.Path
	lowRaw := strings.ToLower(r.URL.RawPath)
	if strings.Contains(rawPath, "..") ||
		strings.Contains(lowRaw, "%2e%2e") ||
		strings.Contains(lowRaw, "%2f") {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	loc := "/pub/" + world + rawPath
	if r.URL.RawQuery != "" {
		loc += "?" + r.URL.RawQuery
	}
	http.Redirect(w, r, loc, http.StatusFound)
}

func (s *server) route(w http.ResponseWriter, r *http.Request) {
	stripClientHeaders(r)
	s.fixForwardedFor(r)

	if r.URL.Path == "/health" {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
		return
	}

	if r.URL.Path == "/" || r.URL.Path == "/pub" {

		if r.URL.Path == "/" {
			if world := s.worldForHost(r.Host); world != "" {
				s.vhostRedirect(w, r, world)
				return
			}
		}
		if s.pubRedir != nil && s.pubRedir.reachable() {
			http.Redirect(w, r, s.pubRedir.url+"/", http.StatusFound)
			return
		}
		http.Redirect(w, r, "/pub/", http.StatusFound)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/pub/") {
		if s.pubRedir != nil &&
			s.pubRedir.reachable() {
			rest := strings.TrimPrefix(r.URL.Path, "/pub")
			if r.URL.RawQuery != "" {
				rest += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, s.pubRedir.url+rest, http.StatusFound)
			return
		}

		web, err := s.webRoutes()
		if err != nil {
			http.Error(w, "Web route state unavailable", http.StatusServiceUnavailable)
			return
		}
		if wr, ok := matchWebRoute(web, r.URL.Path); ok {
			switch wr.Access {
			case "redirect":
				loc := wr.RedirectTo + strings.TrimPrefix(r.URL.Path, wr.PathPrefix)
				if r.URL.RawQuery != "" {
					loc += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, loc, http.StatusFound)
				return
			case "deny":
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			case "auth":
				s.requireAuth(s.files.ServeHTTP)(w, r)
				return
			}
		}
		s.files.ServeHTTP(w, r)
		return
	}

	if r.URL.Path == "/priv" || strings.HasPrefix(r.URL.Path, "/priv/") {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			slotPath := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/priv"), "/")
			if slotPath != "" {
				var gs []string
				if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
					if err := json.Unmarshal([]byte(hdr), &gs); err != nil {
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
				}
				prefix := auth.MatchSlotPrefix(gs, slotPath)
				if prefix == "" {
					slog.Warn("priv forbidden", "sub", r.Header.Get("X-User-Sub"),
						"slot_path", slotPath, "path", r.URL.Path)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
				r = r.WithContext(context.WithValue(r.Context(), staticAllowedKey{}, "priv/"+prefix))
			}
			s.files.ServeHTTP(w, r)
		})(w, r)
		return
	}

	routes, proxies, err := s.routeSnapshot()
	if err != nil {
		http.Error(w, "Route state unavailable", http.StatusServiceUnavailable)
		return
	}

	if r.URL.Path == "/dav" || strings.HasPrefix(r.URL.Path, "/dav/") {
		if rt := MatchRoute(routes, "/dav/"); rt != nil {
			s.dispatchRoute(rt, proxies, w, r)
		} else {
			http.Error(w, "WebDAV not configured", http.StatusNotFound)
		}
		return
	}

	if rt := MatchRoute(routes, r.URL.Path); rt != nil {
		s.dispatchRoute(rt, proxies, w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/auth/") {
		s.requireAuth(s.files.ServeHTTP)(w, r)
		return
	}

	if world := s.worldForHost(r.Host); world != "" {
		s.vhostRedirect(w, r, world)
		return
	}
	http.Redirect(w, r, "/pub"+r.URL.Path, http.StatusFound)
}

func (s *server) dispatchRoute(rt *Route, proxies map[string]*httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	if rt.RedirectTo != "" {
		http.Redirect(w, r, rt.RedirectTo, http.StatusFound)
		return
	}
	rp := proxies[rt.Path]
	if rp == nil {

		slog.Error("proxyd: matched route has no proxy in its own snapshot",
			"path", rt.Path, "url", r.URL.Path)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if rt.Path == "/dav/" {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			s.davRoute(rp, w, r)
		})(w, r)
		return
	}
	switch rt.Auth {
	case "public":
		rp.ServeHTTP(w, r)
	case "optional":

		if a := s.tryAuth(r); a != nil {
			r = a
		} else {
			r = s.attachTransit(r)
		}
		rp.ServeHTTP(w, r)
	default:

		s.requireAuth(rp.ServeHTTP)(w, r)
	}
}

func (s *server) davRoute(rp *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
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

		sorted := append([]string(nil), gs...)
		sort.Strings(sorted)
		for _, g := range sorted {
			if g != "**" {
				group = g
				break
			}
		}
		http.Redirect(w, r, "/dav/"+group+"/", http.StatusFound)
		return
	}

	if !auth.MatchSlot(gs, rest) {
		slog.Warn("dav forbidden", "sub", r.Header.Get("X-User-Sub"),
			"slot_path", rest, "path", r.URL.Path)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if !davAllow(r.Method, rest) {
		slog.Warn("dav blocked", "sub", r.Header.Get("X-User-Sub"),
			"method", r.Method, "path", r.URL.Path)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if davDestMethods[r.Method] {
		dest, ok := davDest(r.Header.Get("Destination"))
		if !ok {
			slog.Warn("dav destination unresolvable", "sub", r.Header.Get("X-User-Sub"),
				"method", r.Method, "path", r.URL.Path,
				"destination", r.Header.Get("Destination"))
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !auth.MatchSlot(gs, dest) {
			slog.Warn("dav destination forbidden", "sub", r.Header.Get("X-User-Sub"),
				"method", r.Method, "slot_path", dest, "path", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		if !davAllow(r.Method, dest) {
			slog.Warn("dav destination blocked", "sub", r.Header.Get("X-User-Sub"),
				"method", r.Method, "slot_path", dest, "path", r.URL.Path)
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		r.Header.Set("Destination", davDestHeader(dest))
	}
	rp.ServeHTTP(w, r)
}

var davDestMethods = map[string]bool{"COPY": true, "MOVE": true}

var davReadMethods = map[string]bool{
	"GET": true, "HEAD": true, "OPTIONS": true, "PROPFIND": true,
}

func davAllow(method, rest string) bool {
	if davReadMethods[method] {
		return true
	}
	parts := strings.Split(rest, "/")

	for _, p := range parts[1:] {
		if p == "logs" {
			return false
		}
	}

	for _, p := range parts {
		if p == ".env" || strings.HasSuffix(p, ".pem") || p == ".git" {
			return false
		}
	}
	return true
}

func davDest(v string) (string, bool) {
	raw, ok := davDestPath(v)
	if !ok {
		return "", false
	}
	decoded, ok := davDestDecode(raw)
	if !ok {
		return "", false
	}
	segs, ok := davDestSegments(decoded)

	if !ok || len(segs) < 2 || segs[0] != "dav" {
		return "", false
	}
	return strings.Join(segs[1:], "/"), true
}

func davDestPath(v string) (string, bool) {
	if v == "" {
		return "", false
	}
	if v[0] != '/' {
		i := strings.Index(v, "://")
		if i <= 0 || !davScheme(v[:i]) {
			return "", false
		}
		authority := v[i+3:]
		j := strings.IndexAny(authority, "/?#")
		if j < 0 || authority[j] != '/' {

			return "/", true
		}
		v = authority[j:]
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		if c == '?' || c == '#' {
			return v[:i], true
		}
		if c <= 0x20 || c >= 0x7f || c == '<' || c == '>' || c == '`' {
			return "", false
		}
	}
	return v, true
}

func davScheme(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		case i > 0 && (c >= '0' && c <= '9' || c == '+' || c == '-' || c == '.'):
		default:
			return false
		}
	}
	return true
}

func davDestDecode(p string) (string, bool) {
	out := make([]byte, 0, len(p))
	for i := 0; i < len(p); i++ {
		if p[i] == '%' && i+2 < len(p) {
			if b, err := hex.DecodeString(p[i+1 : i+3]); err == nil {
				out = append(out, b[0])
				i += 2
				continue
			}
		}
		out = append(out, p[i])
	}
	if !utf8.Valid(out) {
		return "", false
	}
	return string(out), true
}

func davDestSegments(p string) ([]string, bool) {
	var out []string
	for i, seg := range strings.Split(strings.Trim(p, "/"), "/") {
		switch seg {
		case "":
			continue
		case "..":
			return nil, false
		case ".":
			if i == 0 {
				return nil, false
			}
			continue
		}
		out = append(out, seg)
	}
	return out, true
}

func davDestHeader(rest string) string {
	var b strings.Builder
	for _, seg := range strings.Split(rest, "/") {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(seg))
	}
	return b.String()
}

func (s *server) setUserHeaders(r *http.Request, sub, name string, groups []string) *http.Request {
	r2 := s.attachTransit(r)
	r2.Header.Set("X-User-Sub", sub)
	r2.Header.Set("X-User-Name", name)
	groupsJSON := "null"
	if b, err := json.Marshal(groups); err == nil {
		groupsJSON = string(b)
	}
	r2.Header.Set("X-User-Groups", groupsJSON)
	r2.Header.Del("X-User-Sig")
	return r2
}

func (s *server) attachTransit(r *http.Request) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Header.Del("Authorization")
	if s.svc != nil {
		if tok, err := s.svc.Token(r2.Context()); err == nil {
			r2.Header.Set("Authorization", "Bearer "+tok)
		} else {
			slog.Warn("proxyd service token unavailable; forwarding unsigned identity", "err", err)
		}
	}
	return r2
}

func (s *server) groupsForSub(sub string) []string {
	if s.stRoutd == nil {
		return nil
	}
	scopes, err := auth.UserScopes(s.stRoutd, strings.TrimPrefix(sub, "user:"))
	if err != nil {

		slog.Error("proxyd: scope read failed; stamping no groups", "sub", sub, "err", err)
		return nil
	}
	return scopes
}

func (s *server) tryAuth(r *http.Request) *http.Request {
	if s.ks == nil {
		return nil
	}
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
		if sub, err := auth.VerifyHTTP(r, s.ks); err == nil {
			return s.setUserHeaders(r, sub.Sub, sub.Extra["name"], s.groupsForSub(sub.Sub))
		}
	}
	return nil
}

func (s *server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if s.authdProxy == nil {
		http.NotFound(w, r)
		return
	}
	s.authdProxy.ServeHTTP(w, r)
}

func (s *server) tryRefreshViaAuthd(w http.ResponseWriter, r *http.Request) *http.Request {
	if s.cfg.authdURL == "" || s.ks == nil {
		return nil
	}
	cookie, err := r.Cookie("refresh_token")
	if err != nil || cookie.Value == "" {
		return nil
	}
	req, err := http.NewRequestWithContext(r.Context(), "POST", s.cfg.authdURL+"/v1/refresh", nil)
	if err != nil {
		return nil
	}
	req.AddCookie(cookie)
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	var result struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Token == "" {
		return nil
	}
	for _, v := range resp.Header.Values("Set-Cookie") {
		w.Header().Add("Set-Cookie", v)
	}
	rr := r.Clone(r.Context())
	rr.Header.Set("Authorization", "Bearer "+result.Token)
	if sub, err := auth.VerifyHTTP(rr, s.ks); err == nil {
		return s.setUserHeaders(r, sub.Sub, sub.Extra["name"], s.groupsForSub(sub.Sub))
	}
	return nil
}

func (s *server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {

		if s.cfg.authSecret == "" {
			slog.Warn("auth denied", "reason", "auth_secret_unset", "path", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		if a := s.tryAuth(r); a != nil {
			next(w, a)
			return
		}
		if a := s.tryRefreshViaAuthd(w, r); a != nil {
			next(w, a)
			return
		}
		slog.Warn("auth denied", "reason", "no_valid_credential",
			"path", r.URL.Path, "remote", clientIP(r))
		if rt := r.URL.Path; rt != "" && rt != "/" && strings.HasPrefix(rt, "/") &&
			!strings.HasPrefix(rt, "/auth/") {
			if r.URL.RawQuery != "" {
				rt += "?" + r.URL.RawQuery
			}
			http.SetCookie(w, &http.Cookie{
				Name: "auth_return", Value: rt, Path: "/",
				MaxAge: 600, HttpOnly: true, Secure: auth.SecureRequest(r),
				SameSite: http.SameSiteLaxMode,
			})
		}
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	}
}

func main() {
	defer obs.Setup("proxyd", os.Getenv("ARIZUKO_INSTANCE"))()
	defer obs.SetupTraces("proxyd", os.Getenv("ARIZUKO_INSTANCE"))()

	coreCfg, err := core.LoadConfig()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}

	cfg := loadConfig()
	cfg.authSecret = coreCfg.AuthSecret
	cfg.webDir = coreCfg.WebDir

	stRoutd, err := store.OpenOwner(store.OwnerRoutd)
	if err != nil {
		slog.Error("open routd's store", "err", err)
		os.Exit(1)
	}
	defer func() { _ = stRoutd.Close() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var ks *auth.KeySet
	if cfg.authdURL != "" {
		if ks, err = auth.FetchKeys(ctx, cfg.authdURL); err != nil {
			slog.Error("fetch authd keys", "err", err)
			os.Exit(1)
		}
	}

	var svc *auth.TokenSource
	if serviceKey := os.Getenv("AUTHD_SERVICE_KEY"); cfg.authdURL != "" && serviceKey != "" {
		name := core.EnvOr("AUTHD_SERVICE_NAME", "proxyd")
		if svc, err = auth.ServiceToken(cfg.authdURL, name, serviceKey); err != nil {
			slog.Error("build proxyd service token", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Warn("AUTHD_URL/AUTHD_SERVICE_KEY unset; proxyd forwards identity unsigned (local dev)")
	}

	s := newServer(cfg, stRoutd, ks, svc)
	routes, err := s.routes()
	if err != nil {
		slog.Error("read initial routes", "err", err)
		os.Exit(1)
	}

	aud := audit.New(audit.LoadConfig(coreCfg.HostProjectRoot, coreCfg.Name))

	audit.Init(stRoutd.Obj(), coreCfg.Name)
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceREST,
		Resource: "daemons/proxyd",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"port":   cfg.port,
			"routes": len(routes),
		},
	})

	slog.Info("proxyd starting",
		"port", cfg.port, "web", cfg.webDir, "routes", len(routes))

	srv := &http.Server{
		Addr:    cfg.port,
		Handler: s.handler(aud),
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-stop
		slog.Info("proxyd stopping")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := srv.Shutdown(ctx); err != nil {
			slog.Warn("graceful shutdown failed", "err", err)
		}
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("proxyd failed", "err", err)
		os.Exit(1)
	}
}
