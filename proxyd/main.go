package main

import (
	"context"
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

	_ "modernc.org/sqlite"

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
	viteAddr       string
	pubRedirectURL string
	authSecret     string
	authdURL       string // soak: dual-verify ES256 bearers + 302 /auth/* to authd
	routesJSON     string
	trustedProxies []*net.IPNet
	chatAnonDosRPM int
	hostingDomain  string            // world W reached at W.<hostingDomain> → 302 /pub/W/
	vhostAliases   map[string]string // host→world where the label ≠ world name (fab.krons.cx→atlas)
}

func loadConfig() config {
	port := chanlib.EnvOr("PROXYD_LISTEN", "8080")
	if !strings.HasPrefix(port, ":") {
		port = ":" + port
	}
	return config{
		port:           port,
		viteAddr:       chanlib.EnvOr("VITE_ADDR", "http://vited:8080"),
		pubRedirectURL: strings.TrimRight(chanlib.EnvOr("PUB_REDIRECT_URL", ""), "/"),
		authdURL:       strings.TrimRight(os.Getenv("AUTHD_URL"), "/"),
		routesJSON:     os.Getenv("PROXYD_ROUTES_JSON"),
		trustedProxies: parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		chatAnonDosRPM: chanlib.EnvInt("CHAT_ANON_DOS_RPM", 10),
		hostingDomain:  strings.ToLower(strings.TrimSuffix(chanlib.EnvOr("HOSTING_DOMAIN", ""), ".")),
		vhostAliases:   groupfolder.ParseVhostAliases(os.Getenv("WEB_VHOST_ALIASES")),
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

// stripClientHeaders deletes proxyd-owned headers on entry; they are
// repopulated only after auth or chat-token resolution.
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
	st          *store.Store    // messages.db: proxyd_routes, auth_sessions
	stRoutd     *store.Store    // routd.db: acl, auth_users, route_tokens (split ownership)
	rr          *routesResource // stateless route handler; reads routes from DB per request (spec 5/36 no-cache)
	viteProxy   *httputil.ReverseProxy
	authdProxy  *httputil.ReverseProxy // nil when AUTHD_URL unset (local dev)
	ks          *auth.KeySet           // soak: ES256 JWKs (nil when AUTHD_URL unset → HS256-only, exactly as today)
	svc         *auth.TokenSource      // service:proxyd token presented to backends (nil → local dev, X-User-* unsigned)
	chatAnonDOS *rateLimiter           // anon DoS shield, IP-keyed (not metering)
	pubRedir    *pubRedirect
}

// routes / proxies are thin snapshot accessors so the rest of proxyd can
// stay oblivious to the mutex. Slices/maps returned are replaced wholesale
// on mutation, never appended in place, so the caller can use them safely
// without holding the lock.
func (s *server) routes() []Route {
	if s.rr == nil {
		return nil
	}
	r, _ := s.rr.snapshot()
	return r
}

func (s *server) proxies() map[string]*httputil.ReverseProxy {
	if s.rr == nil {
		return nil
	}
	_, p := s.rr.snapshot()
	return p
}

// webRoutes is a per-request snapshot of the web_routes table (no row
// cache, spec 5/36). Agents register rows via the set_web_route MCP
// tool; proxyd honours them on the /pub/* path.
func (s *server) webRoutes() []store.WebRoute {
	if s.rr == nil {
		return nil
	}
	return s.rr.webSnapshot()
}

// matchWebRoute returns the web_route whose path_prefix is the longest
// prefix of urlPath. Ties cannot occur — path_prefix is the PK, so at
// most one row has a given prefix. Returns false when nothing matches.
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

// pubRedirect probes a configured public-docs URL and caches whether
// it's reachable. When reachable, /pub/* is served as an HTTP 302 to
// that URL; when not, the caller falls back to the local viteProxy.
// Probe is a HEAD with a short timeout; result cached for `ttl`.
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
	resp.Body.Close()
	return resp.StatusCode < 500
}

// reachable returns the cached probe result, refreshing if expired.
// One probe per ttl window regardless of caller count.
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

func newServer(cfg config, st, stRoutd *store.Store, ks *auth.KeySet, svc *auth.TokenSource) *server {
	routes := loadInitialRoutes(cfg.routesJSON, st)
	rr := newRoutesResource(st, routes)
	var ap *httputil.ReverseProxy
	if cfg.authdURL != "" {
		ap = proxy(cfg.authdURL)
	}
	return &server{
		cfg:         cfg,
		st:          st,
		stRoutd:     stRoutd,
		rr:          rr,
		viteProxy:   proxy(cfg.viteAddr),
		authdProxy:  ap,
		ks:          ks,
		svc:         svc,
		chatAnonDOS: newRateLimiter(cfg.chatAnonDosRPM, time.Minute),
		pubRedir:    newPubRedirect(cfg.pubRedirectURL),
	}
}

// loadInitialRoutes picks the boot route table. Persistence wins when
// proxyd_routes has rows; otherwise seed from PROXYD_ROUTES_JSON into
// the table. The env var stops being authoritative as soon as the
// table has any row (operator mutations are durable across restarts).
// Spec 6/2 Phase-3 §"Boot config vs runtime mutation".
func loadInitialRoutes(routesJSON string, st *store.Store) []Route {
	if st != nil {
		stored, err := st.AllProxydRoutes()
		if err != nil {
			slog.Error("read proxyd_routes", "err", err)
			os.Exit(1)
		}
		if len(stored) > 0 {
			out := make([]Route, 0, len(stored))
			for _, r := range stored {
				out = append(out, fromStoreRoute(r))
			}
			slog.Info("proxyd routes loaded from db", "count", len(out))
			return out
		}
	}
	routes, err := LoadRoutes(routesJSON)
	if err != nil {
		slog.Error("PROXYD_ROUTES_JSON parse failed", "err", err)
		os.Exit(1)
	}
	if st != nil && len(routes) > 0 {
		ctx := context.Background()
		tx, err := st.DB().BeginTx(ctx, nil)
		if err != nil {
			slog.Error("seed proxyd_routes", "err", err)
			os.Exit(1)
		}
		for _, r := range routes {
			if err := insertProxydRouteTx(ctx, tx, toStoreRoute(r)); err != nil {
				tx.Rollback()
				slog.Error("seed proxyd_routes", "path", r.Path, "err", err)
				os.Exit(1)
			}
		}
		if err := tx.Commit(); err != nil {
			slog.Error("seed proxyd_routes", "err", err)
			os.Exit(1)
		}
		slog.Info("proxyd routes seeded from env", "count", len(routes))
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
	return routes
}

// buildRouteProxy constructs a ReverseProxy for one Route, honouring
// strip_prefix and preserve_headers. Cached per-route so the URL parse +
// Director setup happens once at boot.
func buildRouteProxy(r Route) *httputil.ReverseProxy {
	u, err := url.Parse(r.Backend)
	if err != nil {
		slog.Error("invalid route backend", "path", r.Path, "backend", r.Backend, "err", err)
		return nil
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	orig := rp.Director
	stripPrefix := r.StripPrefix
	prefix := strings.TrimSuffix(r.Path, "/")
	preserveKeys := append([]string(nil), r.PreserveHeaders...)
	backendHost := u.Host
	rp.Director = func(rq *http.Request) {
		saved := map[string]string{}
		for _, k := range preserveKeys {
			if v := rq.Header.Get(k); v != "" {
				saved[k] = v
			}
		}
		orig(rq)
		// Rewrite Host to the backend's hostname (spec §"preserve_headers"):
		// proxyd is not transparent; the inbound Host belongs to proxyd, not
		// the backend. httputil's default Director only updates rq.URL.Host;
		// rq.Host (used by the Transport) is left as the inbound value.
		rq.Host = backendHost
		if stripPrefix && prefix != "" {
			rq.URL.Path = strings.TrimPrefix(rq.URL.Path, prefix)
			if rq.URL.Path == "" {
				rq.URL.Path = "/"
			}
			rq.URL.RawPath = strings.TrimPrefix(rq.URL.RawPath, prefix)
		}
		for k, v := range saved {
			rq.Header.Set(k, v)
		}
	}
	return rp
}

// sweepEvery bounds the full stale-bucket sweep to once per N allow() calls.
// Sweeping every call made allow() O(all-keys): a distinct-IP flood turned
// each anon request into a walk of every live bucket. Amortizing to 1/N keeps
// the hot path O(1) while still reclaiming dead keys often enough to bound the
// map under sustained flood.
const sweepEvery = 256

type rateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string][]time.Time
	calls   int
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{limit: limit, window: window, buckets: make(map[string][]time.Time)}
}

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Reclaim stale buckets once per sweepEvery calls, not every call.
	rl.calls++
	if rl.calls >= sweepEvery {
		rl.calls = 0
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

func (s *server) handler(aud *audit.Audit) http.Handler {
	mux := http.NewServeMux()
	// Proxy /auth/* to authd internally — the browser sees only the public
	// WEB_HOST URL throughout the OAuth flow. Redirecting to authdURL would
	// expose the Docker-internal address (http://authd:8080) to the browser.
	mux.HandleFunc("/auth/", s.handleAuth)
	resreg.RegisterREST(mux, routesResourceDecl(s.rr), callerFromHTTP(s.ks))
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("proxyd", []string{"proxyd_routes"}))
	if obs.MetricsEnabled() {
		mux.Handle("GET /metrics", obs.MetricsHandler())
	}
	mux.HandleFunc("/", s.route)
	return obs.HTTPMiddleware("proxyd")(logging(mux, aud))
}

// clientIP is the real client address: the left-most X-Forwarded-For hop when
// present (the edge proxy sets it), else the direct peer. Without it the
// auth-denied log recorded only the edge-proxy hop (10.0.5.1), hiding the real
// source of a credential-scanning campaign across all three instances.
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
	}
	if peer == "" {
		r.Header.Del("X-Forwarded-For")
		return
	}
	r.Header.Set("X-Forwarded-For", peer)
}

// worldForHost recovers a world name from the request Host. An explicit
// WEB_VHOST_ALIASES entry wins; otherwise the world is derived as the single
// label in `W.<HOSTING_DOMAIN>` via boundary-checked suffix removal — a raw
// HasSuffix would let `notkrons.fiu.wtf` map to `krons`. Returns "" for any
// un-mapped host (bare domain, multi-label subdomain, unknown suffix), which
// falls through to normal routing. Never trusts X-Forwarded-Host.
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

// vhostRedirect 302s a mapped-host request to the world's canonical public
// slot `/pub/<world>/<path>`, preserving sub-path, trailing slash, and query.
// The same `..` / `%2e%2e` / `%2f` rejection that guarded the retired in-place
// rewrite lives here. Location is always a relative `/pub/...` path so an
// attacker-supplied Host cannot open-redirect.
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
		w.Write([]byte(`{"ok":true}`))
		return
	}

	// /pub and /, and /pub/* are kept hand-wired: they involve external
	// redirect probing and websocket-upgrade fall-through, not the static
	// TOML-route forwarding pattern.
	if r.URL.Path == "/" || r.URL.Path == "/pub" {
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			s.viteProxy.ServeHTTP(w, r)
			return
		}
		// A mapped vhost front-doors to its public slot. `/pub` stays reserved
		// in place — only bare `/` redirects, so the follow-up `/pub/W/` lands
		// in the /pub/ branch below and the redirect can't loop.
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
			!strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
			s.pubRedir.reachable() {
			rest := strings.TrimPrefix(r.URL.Path, "/pub")
			if r.URL.RawQuery != "" {
				rest += "?" + r.URL.RawQuery
			}
			http.Redirect(w, r, s.pubRedir.url+rest, http.StatusFound)
			return
		}
		// Agent-registered web_routes: longest matching prefix wins.
		// redirect → 302 with prefix-rewrite into the agent's own slot;
		// deny → 403; auth → gate then proxy; public/no-match → proxy.
		if wr, ok := matchWebRoute(s.webRoutes(), r.URL.Path); ok {
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
				s.requireAuth(s.viteProxy.ServeHTTP)(w, r)
				return
			}
		}
		s.viteProxy.ServeHTTP(w, r)
		return
	}

	// /priv/* — JWT-gated + folder-scoped. Served by the same vited instance
	// as /pub/*, resolving to files under <data>/web/priv/<folder>/. Spec 5/V:
	// agents publish to ~/private_html/, bind-mounted from web/priv/.
	// No path rewrite — vite cwd is <data>/web/, so /priv/X resolves
	// to web/priv/X naturally. Spec 5/38: after auth, the caller must hold
	// a grant covering the target folder (MatchGroups on X-User-Groups).
	if r.URL.Path == "/priv" || strings.HasPrefix(r.URL.Path, "/priv/") {
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			rest := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/priv"), "/")
			folder := strings.SplitN(rest, "/", 2)[0]
			if folder != "" {
				var gs []string
				if hdr := r.Header.Get("X-User-Groups"); hdr != "" {
					if err := json.Unmarshal([]byte(hdr), &gs); err != nil {
						http.Error(w, "Forbidden", http.StatusForbidden)
						return
					}
				}
				if !auth.MatchGroups(gs, folder) {
					slog.Warn("priv forbidden", "sub", r.Header.Get("X-User-Sub"),
						"folder", folder, "path", r.URL.Path)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
			s.viteProxy.ServeHTTP(w, r)
		})(w, r)
		return
	}

	// Bare /dav and /dav/* both route through the /dav/ entry (davRoute picks
	// a group for the bare case). When the route is absent (WEBDAV_ENABLED=
	// false) emit the dedicated 404 rather than the public-redirect fallback.
	if r.URL.Path == "/dav" || strings.HasPrefix(r.URL.Path, "/dav/") {
		if rt := MatchRoute(s.routes(), "/dav/"); rt != nil {
			s.dispatchRoute(rt, w, r)
		} else {
			http.Error(w, "WebDAV not configured", http.StatusNotFound)
		}
		return
	}

	if rt := MatchRoute(s.routes(), r.URL.Path); rt != nil {
		s.dispatchRoute(rt, w, r)
		return
	}

	// /auth/* never matches a TOML route; the mux already served /auth/login
	// before reaching here. Any /auth/* path that falls through (e.g. an
	// unconfigured callback) is treated as a private surface — auth-gate it
	// so it bounces to /auth/login rather than the public /pub fallback.
	if strings.HasPrefix(r.URL.Path, "/auth/") {
		s.requireAuth(s.viteProxy.ServeHTTP)(w, r)
		return
	}

	// Final public catch-all. A mapped vhost front-doors its world's slot;
	// every reserved prefix was dispatched above, so this is the only place
	// the derivation runs — that placement keeps reserved surfaces global and
	// the redirect loop-free (the 302 lands on /pub/W/… → /pub/ branch → stop).
	if world := s.worldForHost(r.Host); world != "" {
		s.vhostRedirect(w, r, world)
		return
	}
	http.Redirect(w, r, "/pub"+r.URL.Path, http.StatusFound)
}

// dispatchRoute applies per-route auth + bespoke handling and forwards via
// the cached ReverseProxy. Bespoke logic for `/chat/`, `/hook/`, and
// `/dav/` lives in dedicated helpers so the generic auth switch stays
// orthogonal.
func (s *server) dispatchRoute(rt *Route, w http.ResponseWriter, r *http.Request) {
	if rt.RedirectTo != "" {
		http.Redirect(w, r, rt.RedirectTo, http.StatusFound)
		return
	}
	rp := s.proxies()[rt.Path]
	if rp == nil {
		http.NotFound(w, r)
		return
	}
	switch rt.Path {
	case "/chat/", "/hook/":
		s.dispatchRouteToken(rp, w, r)
		return
	case "/dav/":
		s.requireAuth(func(w http.ResponseWriter, r *http.Request) {
			s.davRoute(rp, w, r)
		})(w, r)
		return
	}
	switch rt.Auth {
	case "public":
		rp.ServeHTTP(w, r)
	default:
		// `operator` is not yet a distinct gate (spec note: capability tokens
		// pending in 1-auth-standalone.md). Today it resolves to `user` and
		// the daemon enforces operator status via grant check.
		s.requireAuth(rp.ServeHTTP)(w, r)
	}
}

// dispatchRouteToken resolves the URL's route token to a folder and
// stamps X-Folder/X-Group-Name/X-Chat-Token before forwarding (the
// service:proxyd transit bearer proves the stamp to webd — proxyd is
// the sole setter and strips any inbound X-Chat-*). Spec 5/W. Auth is
// optional; a valid JWT stamps user
// identity. Anon callers pass through an IP-keyed DoS shield (not
// metering — cost-cap governance handles spend per spec 5/34);
// authenticated callers skip the throttle entirely.
func (s *server) dispatchRouteToken(rp *httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	if a := s.tryAuth(r); a != nil {
		r = a
	} else {
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !s.chatAnonDOS.allow(remoteIP) {
			http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
			return
		}
	}
	// Path shapes: /chat/<token>/..., /hook/<token>, /chat/stream?token=...
	var token string
	if r.URL.Path == "/chat/stream" {
		token = r.URL.Query().Get("token")
	} else {
		for _, prefix := range []string{"/chat/", "/hook/"} {
			if strings.HasPrefix(r.URL.Path, prefix) {
				token = strings.SplitN(strings.TrimPrefix(r.URL.Path, prefix), "/", 2)[0]
				break
			}
		}
	}
	// route_tokens lives in routd.db (stRoutd) — split ownership
	if token != "" && s.stRoutd != nil {
		if row, ok := s.stRoutd.LookupRouteToken(token); ok {
			folder := groupfolder.JidFolder(row.JID)
			r = r.Clone(r.Context())
			r.Header.Set("X-Folder", folder)
			r.Header.Set("X-Group-Name", groupfolder.NameOf(folder))
			r.Header.Set("X-Chat-Token", token)
		}
	}
	rp.ServeHTTP(w, r)
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
		// Sort to make the landing pick deterministic — map iteration
		// upstream produces the groups claim in arbitrary order.
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

	group := strings.SplitN(rest, "/", 2)[0]
	if !auth.MatchGroups(gs, group) {
		slog.Warn("dav forbidden", "sub", r.Header.Get("X-User-Sub"),
			"group", group, "path", r.URL.Path)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	if !davAllow(r.Method, rest) {
		slog.Warn("dav blocked", "sub", r.Header.Get("X-User-Sub"),
			"method", r.Method, "path", r.URL.Path)
		http.Error(w, "Forbidden", http.StatusForbidden)
		return
	}
	rp.ServeHTTP(w, r)
}

// davReadMethods are HTTP/WebDAV verbs that don't mutate the workspace.
var davReadMethods = map[string]bool{
	"GET": true, "HEAD": true, "OPTIONS": true, "PROPFIND": true,
}

// davAllow enforces two protections on top of group-scoped routing:
//   - sensitive paths (`.env`, `*.pem`, anything under `.git/`) cannot be
//     written via WebDAV.
//   - any path under `<group>/logs/` is read-only.
//
// rest is the path after `/dav/`, e.g. `myworld/logs/foo.log` or
// `myworld/.env`. Returns false to block.
func davAllow(method, rest string) bool {
	if davReadMethods[method] {
		return true
	}
	parts := strings.Split(rest, "/")
	// logs/ read-only — `<group>/logs` or `<group>/logs/...`
	if len(parts) >= 2 && parts[1] == "logs" {
		return false
	}
	// Sensitive-path write block on any segment.
	for _, p := range parts {
		if p == ".env" || strings.HasSuffix(p, ".pem") || p == ".git" {
			return false
		}
	}
	return true
}

// setUserHeaders stamps the verified caller's identity onto the forwarded
// request and proves the channel to backends. Identity travels in X-User-*; the
// proof is proxyd's own ES256 service token (Authorization: Bearer
// service:proxyd), which backends verify (auth.ProxydTransit) before trusting
// the stamped headers. With no service token (local dev, AUTHD_URL/key unset)
// the headers go unsigned: backends with no JWKS configured pass them through as
// local-dev, exactly as an unsigned request always was.
func (s *server) setUserHeaders(r *http.Request, sub, name string, groups []string) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-User-Sub", sub)
	r2.Header.Set("X-User-Name", name)
	groupsJSON := "null"
	if b, err := json.Marshal(groups); err == nil {
		groupsJSON = string(b)
	}
	r2.Header.Set("X-User-Groups", groupsJSON)
	r2.Header.Del("X-User-Sig")
	// Clear the caller's inbound Authorization unconditionally: identity now
	// rides X-User-*, and proxyd's own service token is the channel proof. If we
	// left the caller's bearer in place, a backend behind RequireSignedOrBearer
	// would re-derive identity FROM that bearer (clobbering the stamped sub) —
	// and on the no-service-token path it must truly forward unsigned.
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

// groupsForSub resolves a verified sub to its grant patterns (X-User-Groups).
// ES256 subs are prefixed (`user:google:123`); grant rows key on the bare sub
// (`google:123`) per spec 5/1 § "sub prefix rule". Strip a leading `user:`
// before the lookup so ES256 and HS256 subs map to the same grants.
// Reads from routd.db (stRoutd) — acl lives there in the split topology.
func (s *server) groupsForSub(sub string) []string {
	if s.stRoutd == nil {
		return nil
	}
	return s.stRoutd.UserScopes(strings.TrimPrefix(sub, "user:"))
}

// tryAuth returns an identity-stamped request if the caller has a valid
// Bearer JWT or refresh-token cookie; otherwise nil.
//
// Bearer verify is dual: HS256 (auth.VerifyJWT, legacy cookie/session) first,
// then ES256 (auth.VerifyHTTP) against authd's JWKs when AUTHD_URL is set. Either
// success stamps the same X-User-* headers; setUserHeaders attaches proxyd's own
// service:proxyd bearer as the backend transit proof. With AUTHD_URL unset, ks is
// nil and this is HS256-only.
func (s *server) tryAuth(r *http.Request) *http.Request {
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
		if c, err := auth.VerifyJWT([]byte(s.cfg.authSecret), strings.TrimPrefix(hdr, "Bearer ")); err == nil {
			return s.setUserHeaders(r, c.Sub, c.Name, c.Groups)
		}
		if s.ks != nil {
			if sub, err := auth.VerifyHTTP(r, s.ks); err == nil {
				return s.setUserHeaders(r, sub.Sub, sub.Extra["name"], s.groupsForSub(sub.Sub))
			}
		}
	}
	if s.st == nil {
		return nil
	}
	cookie, err := r.Cookie("refresh_token")
	if err != nil {
		return nil
	}
	// auth_sessions lives in messages.db (st)
	sess, ok := s.st.AuthSession(auth.HashToken(cookie.Value))
	if !ok || !time.Now().Before(sess.ExpiresAt) {
		return nil
	}
	// auth_users + acl live in routd.db (stRoutd) — split ownership
	if s.stRoutd == nil {
		return nil
	}
	// Resolve canonical at the cookie path too — refresh sessions are
	// bound to the sub at creation time, but the user may have linked
	// since. Single source of truth: store.CanonicalSub.
	canonical := s.stRoutd.CanonicalSub(sess.UserSub)
	u, ok := s.stRoutd.AuthUserBySub(canonical)
	if !ok {
		return nil
	}
	return s.setUserHeaders(r, u.Sub, u.Name, s.stRoutd.UserScopes(u.Sub))
}

// handleAuth proxies /auth/* to authd without redirecting. A redirect would
// send the browser to http://authd:8080 (Docker-internal), which is unreachable
// from outside the container network. Proxying keeps the public WEB_HOST URL
// visible to the browser throughout the OAuth flow.
// Falls through to 404 in local dev (authdURL unset, authdProxy nil).
func (s *server) handleAuth(w http.ResponseWriter, r *http.Request) {
	if s.authdProxy == nil {
		http.NotFound(w, r)
		return
	}
	s.authdProxy.ServeHTTP(w, r)
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
		slog.Warn("auth denied", "reason", "no_valid_credential",
			"path", r.URL.Path, "remote", clientIP(r))
		if rt := r.URL.Path; rt != "" && rt != "/" && strings.HasPrefix(rt, "/") &&
			!strings.HasPrefix(rt, "/auth/") {
			if r.URL.RawQuery != "" {
				rt += "?" + r.URL.RawQuery
			}
			secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
			http.SetCookie(w, &http.Cookie{
				Name: "auth_return", Value: rt, Path: "/",
				MaxAge: 600, HttpOnly: true, Secure: secure,
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

	st, err := store.Open(coreCfg.StoreDir)
	if err != nil {
		slog.Error("open store", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// routd.db owns acl/auth_users/route_tokens in the split topology (spec 5/5).
	// proxyd reads those for scope stamping + route token resolution; a frozen
	// messages.db twin would make post-cutover grants invisible to auth.
	stRoutd, err := store.OpenRoutd(coreCfg.StoreDir)
	if err != nil {
		slog.Error("open routd.db", "err", err)
		os.Exit(1)
	}
	defer stRoutd.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Soak: dual-verify ES256 bearers alongside HS256. When AUTHD_URL is set,
	// fetch authd's public JWKs so tryAuth can accept ES256 tokens; unset →
	// ks stays nil and proxyd is HS256-only, exactly as today.
	var ks *auth.KeySet
	if cfg.authdURL != "" {
		if ks, err = auth.FetchKeys(ctx, cfg.authdURL); err != nil {
			slog.Error("fetch authd keys", "err", err)
			os.Exit(1)
		}
	}

	// HMAC retire step 2: proxyd presents its own service:proxyd ES256 token to
	// backends (Authorization: Bearer) as the channel proof for the X-User-*
	// headers it stamps — replacing the X-User-Sig HMAC. Exchanged lazily from
	// AUTHD_SERVICE_KEY at AUTHD_URL (same pattern as timed/onbod). Unset (local
	// dev) → svc nil, identity forwarded unsigned exactly as an anon request.
	var svc *auth.TokenSource
	if serviceKey := os.Getenv("AUTHD_SERVICE_KEY"); cfg.authdURL != "" && serviceKey != "" {
		name := chanlib.EnvOr("AUTHD_SERVICE_NAME", "proxyd")
		if svc, err = auth.ServiceToken(cfg.authdURL, name, serviceKey); err != nil {
			slog.Error("build proxyd service token", "err", err)
			os.Exit(1)
		}
	} else {
		slog.Warn("AUTHD_URL/AUTHD_SERVICE_KEY unset; proxyd forwards identity unsigned (local dev)")
	}

	s := newServer(cfg, st, stRoutd, ks, svc)

	aud := audit.New(audit.LoadConfig(coreCfg.HostProjectRoot, coreCfg.Name))

	audit.Init(st.DB(), coreCfg.Name)
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem,
		Action:   "daemon.start",
		Actor:    "system",
		Surface:  audit.SurfaceREST,
		Resource: "daemons/proxyd",
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"port":   cfg.port,
			"routes": len(s.routes()),
		},
	})

	slog.Info("proxyd starting",
		"port", cfg.port, "vite", cfg.viteAddr, "routes", len(s.routes()))

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
		srv.Shutdown(ctx)
	}()

	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		slog.Error("proxyd failed", "err", err)
		os.Exit(1)
	}
}
