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
	viteAddr       string
	pubRedirectURL string
	authSecret     string
	authdURL       string // soak: dual-verify ES256 bearers + 302 /auth/* to authd
	trustedProxies []*net.IPNet
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
		trustedProxies: parseTrustedProxies(os.Getenv("TRUSTED_PROXIES")),
		hostingDomain:  strings.ToLower(strings.TrimSuffix(chanlib.EnvOr("HOSTING_DOMAIN", ""), ".")),
		vhostAliases:   groupfolder.ParseVhostAliases(os.Getenv("WEB_VHOST_ALIASES")),
	}
}

// parseTrustedProxies parses comma-separated CIDRs; bare IP → /32 or /128.
// Empty = no client trusted; XFF is always replaced with the connection peer.
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

// stripClientHeaders deletes proxyd-owned headers on entry; X-User-* is
// repopulated only after auth. The X-Chat-* / X-Folder group has no reader
// left — proxyd stopped resolving route tokens (specs/6/22 step 1) — but the
// strip stays: owning the invariant "no client-supplied identity header
// survives ingress" is proxyd's job, so a future backend that reads one cannot
// be fed it from outside.
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
	stRoutd     *store.Store    // routd.db: proxyd_routes, acl, user_profiles (split ownership; scope lookup reads one DB)
	rr          *routesResource // stateless route handler; reads routes from DB per request (spec 5/8 no-cache)
	viteProxy   *httputil.ReverseProxy
	authdProxy  *httputil.ReverseProxy // nil when AUTHD_URL unset (local dev)
	ks          *auth.KeySet           // authd's ES256 JWKs; nil when AUTHD_URL unset, and then tryAuth authenticates nobody
	svc         *auth.TokenSource      // service:proxyd token presented to backends (nil → local dev, X-User-* unsigned)
	pubRedir    *pubRedirect
	warnedEdges sync.Map // peer → struct{}, so an untrusted edge warns once, not per request
}

// untrustedEdgeOnce reports a forwarding peer missing from TRUSTED_PROXIES.
func (s *server) untrustedEdgeOnce(peer string) {
	if _, seen := s.warnedEdges.LoadOrStore(peer, struct{}{}); seen {
		return
	}
	slog.Warn("untrusted edge proxy: X-Forwarded-For discarded, client identity is the hop",
		"peer", peer, "fix", "add "+peer+" to TRUSTED_PROXIES")
}

// routeSnapshot returns ONE consistent view of the route table and the proxies
// built from it. Every dispatch takes it once and passes it down: the route a
// request matched and the backend it is forwarded to must come from the same
// read of `proxyd_routes`, or a concurrent mutation pairs one route's auth
// policy with another's backend (BUGS P3). Slices/maps returned are never
// mutated in place, so a caller can use them without holding a lock.
func (s *server) routeSnapshot() ([]Route, map[string]*httputil.ReverseProxy) {
	if s.rr == nil {
		return nil, nil
	}
	return s.rr.snapshot()
}

// routes is the read-only view for callers that need the table but no backend
// (startup logging, /health counts). Dispatch must use routeSnapshot.
func (s *server) routes() []Route {
	r, _ := s.routeSnapshot()
	return r
}

// webRoutes is a per-request snapshot of the web_routes table (no row
// cache, spec 5/8). Agents register rows via the set_web_route MCP
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
		viteProxy:  proxy(cfg.viteAddr),
		authdProxy: ap,
		ks:         ks,
		svc:        svc,
		pubRedir:   newPubRedirect(cfg.pubRedirectURL),
	}
}

// loadInitialRoutes reads the boot route table. proxyd_routes is the only source:
// `arizuko generate` writes the release's routes into it and proxyd re-reads it
// per request. There is no env var and no seeding, so there is no window in which
// config stops applying — PROXYD_ROUTES_JSON was seed-once and cost krons seven
// routes (BUGS L45). Dispatch takes a fresh snapshot per request.
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

// mux builds proxyd's routing table, catch-all included. Split out of handler
// so a test can probe the real ServeMux: handler returns the middleware-wrapped
// http.Handler, and a wrapped handler answers every path — including the ones
// only the catch-all serves — so the doc-vs-mux guard cannot tell a real mount
// from the fallback through it (BUGS F40).
func (s *server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	// Proxy /auth/* to authd internally — the browser sees only the public
	// WEB_HOST URL throughout the OAuth flow. Redirecting to authdURL would
	// expose the Docker-internal address (http://authd:8080) to the browser.
	// The forward-auth subrequest surface (6/22 step 3), mounted BEFORE the
	// /auth/ proxy so it is answered here rather than forwarded to authd.
	mux.HandleFunc("/auth/forward", s.handleForwardAuth)
	mux.HandleFunc("/auth/", s.handleAuth)
	resreg.RegisterREST(mux, routesResourceDecl(s.rr), callerFromHTTP(s.ks))
	mux.HandleFunc("GET /openapi.json", resreg.OpenAPIHandler("proxyd", mux))
	// The operator dashboard (spec 6/28). requireAuth is wrapped HERE because a
	// path on this mux never reaches s.route, which is where a routed request
	// gets authenticated — a bare mount would serve the route table, and its
	// write forms, to an unauthenticated caller.
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

// clientIP is the real client address: the left-most X-Forwarded-For hop when
// present, else the direct peer. Without it the auth-denied log recorded only the
// edge-proxy hop, hiding the real source of a credential-scanning campaign. Every
// per-source decision uses it — the anon chat DoS shield read RemoteAddr, so
// behind nginx all anonymous visitors shared one 10/min bucket.
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
		// An edge proxy is forwarding but we do not trust it, so its own address
		// replaces the client's below. Every per-source decision then keys on one
		// value fleet-wide: the anon chat shield shares a single bucket, the
		// auth-denied log names the hop instead of the scanner, and webd's
		// anonSender collapses all visitors into one identity. Silent until
		// anteval hit the shared bucket (2026-08-16), so say it once per peer.
		s.untrustedEdgeOnce(peer)
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

	// /priv/* — JWT-gated + folder-scoped, served by the same vited instance as
	// /pub/* and resolving under <data>/web/priv/<folder>/. No path rewrite: vite
	// cwd is <data>/web/. After auth the caller must hold a grant covering the
	// folder that owns the slot (auth.MatchSlot). This used to cut the path at its
	// FIRST segment, which is wrong in both directions: a folder is multi-segment,
	// so `atlas/search` could not read its own slot, while a scope of `atlas/*`
	// read nothing at all.
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
				if !auth.MatchSlot(gs, slotPath) {
					slog.Warn("priv forbidden", "sub", r.Header.Get("X-User-Sub"),
						"slot_path", slotPath, "path", r.URL.Path)
					http.Error(w, "Forbidden", http.StatusForbidden)
					return
				}
			}
			s.viteProxy.ServeHTTP(w, r)
		})(w, r)
		return
	}

	// ONE snapshot for the whole dispatch. Matching used to read the route
	// table and proxy selection used to read it AGAIN, so a request could be
	// matched against one version of `proxyd_routes` and forwarded (or 404'd)
	// against another: old auth policy paired with a new backend, or a false
	// 404 when the matched path was gone by the second read (BUGS P3). It also
	// halves the per-request DB reads and proxy construction.
	routes, proxies := s.routeSnapshot()

	// Bare /dav and /dav/* both route through the /dav/ entry (davRoute picks
	// a group for the bare case). When the route is absent (WEBDAV_ENABLED=
	// false) emit the dedicated 404 rather than the public-redirect fallback.
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

// dispatchRoute applies per-route auth + bespoke handling and forwards via the
// cached ReverseProxy. It takes the proxy map from the SAME snapshot rt was
// matched in — never re-reads it — so route and backend cannot come from two
// versions of the table (BUGS P3).
func (s *server) dispatchRoute(rt *Route, proxies map[string]*httputil.ReverseProxy, w http.ResponseWriter, r *http.Request) {
	if rt.RedirectTo != "" {
		http.Redirect(w, r, rt.RedirectTo, http.StatusFound)
		return
	}
	rp := proxies[rt.Path]
	if rp == nil {
		// Unreachable: snapshot() builds a proxy for every non-redirect route in
		// the same read rt was matched from, and the redirect case returned
		// above. It used to be reachable — and answered 404 — because proxy
		// selection re-read the table (BUGS P3). A 404 here would blame the
		// caller for a proxyd bug, so say what it is.
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
		// Identify when the caller offers a credential, forward anonymously when they
		// do not — the route-token surface, where the token is the credential and a
		// stamped sub only decides attribution. Either way the request carries
		// proxyd's transit proof. This is a property of the route, not a path proxyd
		// hardcodes (6/22 step 1).
		if a := s.tryAuth(r); a != nil {
			r = a
		} else {
			r = s.attachTransit(r)
		}
		rp.ServeHTTP(w, r)
	default:
		// `operator` is not yet a distinct gate (spec note: capability tokens
		// pending in 1-auth-standalone.md). Today it resolves to `user` and
		// the daemon enforces operator status via grant check.
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

	// Same question, same answer as /priv: dufs serves <data>/groups as /data,
	// so /dav/<folder>/<file> walks a tree that nests exactly like the web
	// slots — groups/atlas holds groups/atlas/search, and folder atlas's
	// container home IS groups/atlas. Cutting at the first segment 403'd a
	// multi-segment folder on its own files.
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
		// The backend resolves the header proxyd wrote, never the one the
		// caller sent, so the authorized path IS the written path.
		r.Header.Set("Destination", davDestHeader(dest))
	}
	rp.ServeHTTP(w, r)
}

// davDestMethods are the verbs whose write target is the Destination header
// instead of the request line. Measured against davd's binary (dufs 0.46):
// COPY and MOVE read the header, every other verb it serves writes only where
// it was addressed, and the match is case-sensitive — a lowercase `move` is a
// 405 there, so an exact match here refuses exactly what the backend acts on.
var davDestMethods = map[string]bool{"COPY": true, "MOVE": true}

// davReadMethods are HTTP/WebDAV verbs that don't mutate the workspace.
var davReadMethods = map[string]bool{
	"GET": true, "HEAD": true, "OPTIONS": true, "PROPFIND": true,
}

// davAllow enforces two protections on top of group-scoped routing: sensitive
// paths (`.env`, `*.pem`, anything under `.git/`) cannot be written via WebDAV,
// and any path under `<group>/logs/` is read-only. rest is a path after
// `/dav/` — the request line's, and for COPY/MOVE the Destination's too.
// Returns false to block.
func davAllow(method, rest string) bool {
	if davReadMethods[method] {
		return true
	}
	parts := strings.Split(rest, "/")
	// logs/ read-only — `<group>/logs` or `<group>/logs/...` at ANY folder
	// depth. The group boundary is not fixed at index 1: `main/trading` is one
	// folder, so `main/trading/logs` is a group's log directory too.
	for _, p := range parts[1:] {
		if p == "logs" {
			return false
		}
	}
	// Sensitive-path write block on any segment.
	for _, p := range parts {
		if p == ".env" || strings.HasSuffix(p, ".pem") || p == ".git" {
			return false
		}
	}
	return true
}

// davDest resolves a WebDAV Destination header to the path dufs will write,
// in the same shape as `rest`: the part under `/dav/`. It returns false when
// the header names nothing this route serves — unparseable, a `..` hop, or
// outside `/dav/`.
//
// WebDAV has two write targets and only the request line was modelled, so a
// caller granted `coach` moved `/dav/coach/notes.md` onto
// `Destination: /main/trading/CLAUDE.md` and both guards passed (BUGS L114).
// Every step below mirrors dufs 0.46, measured against the shipped
// arizuko-davd image: parse as a URI and discard scheme and authority
// (`http://evil.example/b/x` writes `b/x`), take the path, percent-decode it
// ONCE, then walk it with Rust `Path::components` semantics. Decoding precedes
// the split, so `%2F` is a separator and `%2e%2e` is a parent hop. davRoute
// then rewrites the header from this result (davDestHeader), which is what
// makes the guard authoritative: a normalization that diverged from the
// backend would be a bypass rather than a fix, and the backend never sees the
// caller's spelling.
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
	// `/dav` is this route's own prefix, which buildRouteProxy strips off the
	// forwarded URL; a conforming client derives Destination from the request
	// URI (RFC 4918 §10.3), so it carries the prefix too.
	if !ok || len(segs) < 2 || segs[0] != "dav" {
		return "", false
	}
	return strings.Join(segs[1:], "/"), true
}

// davDestPath extracts the URI path the way hyper's `Uri` does, which is what
// dufs feeds its resolver. A value starting with `/` is origin-form and is a
// path in full: `//main/x` is the path `//main/x`, NOT authority `main` — that
// is where net/url disagrees, and reading its Host would have left the write
// unguarded. Anything else must be absolute-form, whose authority is dropped.
// The byte filter is hyper's own, measured over the whole ASCII range: visible
// ASCII less `<`, `>` and backtick, with `?` and `#` ending the path.
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
			// Absolute-form carrying no path; hyper reports "/" for it.
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

// davScheme reports whether s is a URI scheme: ALPHA *( ALPHA / DIGIT / "+" /
// "-" / "." ), RFC 3986 §3.1.
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

// davDestDecode percent-decodes the way dufs's decode_uri does: `%` and two
// hex digits is one byte, a `%` without them is itself — net/url's unescape
// rejects that instead, and refusing a name dufs accepts would break a move
// the caller is entitled to. Invalid UTF-8 fails, as it does there.
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

// davDestSegments splits a decoded path the way Rust's `Path::components`
// does, which is how dufs turns it into a file path: repeated separators
// collapse, a trailing slash drops, `.` is normalized away everywhere but the
// front — where it is a CurDir component dufs refuses — and `..` is a ParentDir
// component it refuses wherever it stands. `..` inside a segment (`a..b`) is an
// ordinary name.
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

// davDestHeader renders the Destination proxyd forwards: the authorized path,
// escaped segment by segment so dufs decodes back exactly these names. `/dav`
// is absent because buildRouteProxy strips the same prefix off the request
// line — header and request line must name one tree.
func davDestHeader(rest string) string {
	var b strings.Builder
	for _, seg := range strings.Split(rest, "/") {
		b.WriteByte('/')
		b.WriteString(url.PathEscape(seg))
	}
	return b.String()
}

// setUserHeaders stamps the verified caller's identity onto the forwarded request
// and proves the channel to backends. Identity travels in X-User-*; the proof is
// proxyd's own ES256 service token, which backends verify before trusting the
// stamped headers. With no service token (local dev) the headers go unsigned.
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

// attachTransit replaces the caller's Authorization with proxyd's own
// service:proxyd bearer — the proof a backend checks before trusting anything
// proxyd stamped. Clearing the inbound header is unconditional: identity rides
// X-User-*, so a backend that re-derived identity FROM the caller's bearer would
// clobber the stamped sub.
//
// It runs for identified AND anonymous callers on `optional` routes. Running only
// inside setUserHeaders left an anonymous /chat/ request reaching webd with no
// proof it had transited proxyd, which is why the chat widget's live stream
// answered 403 to every visitor on any instance with a JWKS (specs/6/22 step 1).
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

// groupsForSub resolves a verified sub to its grant patterns (X-User-Groups).
// ES256 subs are prefixed (`user:google:123`); grant rows key on the bare sub
// (`google:123`) per spec 5/1 § "sub prefix rule". Strip a leading `user:`
// before the lookup so a bearer's sub and its grant row key match.
// Reads from routd.db (stRoutd) — acl lives there in the split topology.
func (s *server) groupsForSub(sub string) []string {
	if s.stRoutd == nil {
		return nil
	}
	scopes, err := auth.UserScopes(s.stRoutd, strings.TrimPrefix(sub, "user:"))
	if err != nil {
		// Stamp no groups — the backend then denies, which is the right direction.
		// But an unreadable acl table and a user who holds nothing produced the
		// SAME header, so the resulting 403s had no cause anywhere (BUGS A4).
		slog.Error("proxyd: scope read failed; stamping no groups", "sub", sub, "err", err)
		return nil
	}
	return scopes
}

// tryAuth returns an identity-stamped request if the caller has a valid Bearer
// JWT; otherwise nil. A refresh_token cookie is NOT an identity here — requireAuth
// trades it for an access token. Bearer verify is ES256 only, against authd's JWKs.
//
// ES256 is the only algorithm accepted, and authd is its only signer. A shared
// secret verified here would turn one AUTH_SECRET leak into arbitrary identity
// forgery, since proxyd is the sole stamper of X-User-* (BUGS L6).
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

// tryRefreshViaAuthd exchanges a refresh_token cookie for a fresh ES256 access
// JWT by calling authd's POST /v1/refresh. Forwards the rotated Set-Cookie to
// the browser so the token stays alive across subsequent requests. Returns nil
// when authd is unconfigured, no cookie present, or the exchange fails.
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
	defer resp.Body.Close()
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
		// AUTH_SECRET keys the OAuth CSRF state and verifies no bearer
		// (BUGS L6), so the login flow cannot complete without it. Denying
		// here keeps the failure at the door rather than mid-flow.
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

	// routd.db owns proxyd_routes/acl/user_profiles/route_tokens in the split
	// topology (spec 5/5), plus audit_log (routd migration 0016). proxyd reads
	// those for scope resolution (verified sub → scopes) + route resolution, and
	// writes its audit rows here too (audit.Init below) — one store, no
	// messages.db straddle.
	//
	// store.OpenOwner is the door: on an instance served from a DynamoDB
	// endpoint it dials routd's tables there, and otherwise it opens the file
	// DB_PATH names — which may be a postgres:// URL, the same env gitd, slakd
	// and dashd already read (spec 6/37 step 6). Unset, it is the owner file
	// under the store dir.
	stRoutd, err := store.OpenOwner(store.OwnerRoutd)
	if err != nil {
		slog.Error("open routd's store", "err", err)
		os.Exit(1)
	}
	defer stRoutd.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Fetch authd's public JWKs so tryAuth can verify ES256 bearers offline.
	// With AUTHD_URL unset (local dev) ks stays nil and tryAuth authenticates
	// nobody, so every route that needs an identity falls through to login.
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

	s := newServer(cfg, stRoutd, ks, svc)

	aud := audit.New(audit.LoadConfig(coreCfg.HostProjectRoot, coreCfg.Name))

	// audit_log lives in routd.db (its owner, routd migration 0016); proxyd is
	// FS-mounted but owns no DB of its own, so it writes into the owner DB via
	// the same stRoutd handle used for routing + auth (no proxyd.db, no
	// messages.db straddle — BUGS.md Y1, split write-discipline).
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
