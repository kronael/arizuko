package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/kronael/arizuko/compose"
)

// service describes one daemon tile on the cockpit services hub. host is the
// Docker-network name probed at <host>:8080<probe>. Probe overrides the default
// /health path (davd has no /health — it answers GET /).
//
// There is deliberately no `Built` field. Whether a tile LINKS is read from the
// live route table (see shouldLink), because 6/29 makes the route entry the
// registration: "the daemon list comes from the proxyd /dash/<daemon>/ route
// entries — no registry, no autodiscovery". The bool that used to sit here was
// a second record of that fact and it drifted from the tree in five of eight
// rows, in both directions at once — linking at pages that did not exist while
// hiding two that did.
type service struct {
	Name  string
	Host  string
	Dash  string
	Desc  string
	Probe string
}

// services is the static daemon list the hub probes. No autodiscovery — route
// entry IS registration (spec 6/28); adding a daemon here is a deliberate edit.
//
// This list is the set of daemons the hub KNOWS ABOUT and probes. It does not
// decide which of them are reachable — the route table does.
var services = []service{
	{"routd", "routd", "/dash/routd/", "message router, route table, breakers", ""},
	{"runed", "runed", "/dash/runed/", "agent container runs and tokens", ""},
	{"authd", "authd", "/dash/authd/", "signing keys and who is signed in", ""},
	{"proxyd", "proxyd", "/dash/proxyd/", "auth-gated reverse proxy", ""},
	{"onbod", "onbod", "/dash/onbod/", "onboarding queue, gates, invites", ""},
	{"timed", "timed", "/dash/timed/", "scheduled tasks and ticks", ""},
	{"webd", "webd", "/dash/webd/", "web chat widget and routes", ""},
	{"davd", "davd", "/dash/davd/", "WebDAV workspace access", "/"},
	{"ttsd", "ttsd", "/dash/ttsd/", "text-to-speech proxy", ""},
	// Channel adapters (spec 6/31, 6/32): each serves the SHARED chanlib
	// dashboard from the mux it already builds, and each route is gated by the
	// adapter's own credential, so a tile links exactly when the adapter is
	// deployed.
	{"slakd", "slakd", "/dash/slakd/", "Slack adapter", ""},
	{"teled", "teled", "/dash/teled/", "Telegram adapter", ""},
	{"discd", "discd", "/dash/discd/", "Discord adapter", ""},
	{"emaid", "emaid", "/dash/emaid/", "Email adapter", ""},
	{"bskyd", "bskyd", "/dash/bskyd/", "Bluesky adapter", ""},
	{"reditd", "reditd", "/dash/reditd/", "Reddit adapter", ""},
	{"linkd", "linkd", "/dash/linkd/", "LinkedIn adapter", ""},
	// The two TypeScript adapters re-implement the same contract in their own
	// source (6/31): they cannot import chanlib.
	{"whapd", "whapd", "/dash/whapd/", "WhatsApp adapter", ""},
	{"twitd", "twitd", "/dash/twitd/", "Twitter adapter", ""},
}

// healthTimeout caps each /health probe. The grid probes all daemons
// concurrently, so the whole page waits at most this long.
const healthTimeout = 500 * time.Millisecond

// statusOK/Err/Unknown classify a tile's status dot. ok = /health 2xx;
// err = reachable-but-unhealthy OR refused/timeout (daemon down in production);
// unknown = DNS failure (container not deployed or local-dev name mismatch).
//
// Three, not four. `6/29` and `6/30` asked for a `warn` state meaning "up but
// scope-denied"; it has no subject, because the probe is `/health`, which every daemon
// mounts before auth by design. Both specs withdraw the criterion rather than leave it
// open (2026-10-08) — a fourth branch no probe can reach is dead code, not a gap.
const (
	statusOK      = "ok"
	statusErr     = "err"
	statusUnknown = "unknown"
)

// probeHealth GETs http://<host>:8080<path> with a short timeout and maps the
// outcome to an ok/err/unknown status. path defaults to /health when empty.
func probeHealth(host, path string) string {
	if path == "" {
		path = "/health"
	}
	c := &http.Client{Timeout: healthTimeout}
	resp, err := c.Get("http://" + host + ":8080" + path)
	status := classifyHealth(resp, err)
	// A client-deadline timeout can hide a DNS failure: when the HTTP deadline
	// fires before the resolver returns, the error is an opaque timeout, not a
	// *net.DNSError. Disambiguate with an explicit resolve so an undeployed
	// container (name doesn't resolve) is unknown, not err.
	if status == statusErr && err != nil && !hostResolves(host) {
		return statusUnknown
	}
	return status
}

// hostResolves reports whether host resolves to at least one address within the
// probe timeout. A DNS failure (no such host, or resolver timeout) = false.
func hostResolves(host string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	return err == nil && len(addrs) > 0
}

// classifyHealth maps a /health GET outcome to a status.
// DNS failure (host unresolvable) = unknown (container not deployed).
// Connection refused or client timeout = deployed but daemon down = err.
func classifyHealth(resp *http.Response, err error) string {
	if err == nil {
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return statusOK
		}
		return statusErr
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return statusUnknown
	}
	return statusErr
}

// shouldLink reports whether a tile links to its control plane.
//
// The answer is the route table: a /dash/<daemon>/ route exists only for a
// daemon that serves one, so a tile links exactly when clicking it arrives
// somewhere. 6/29: "the daemon list comes from the proxyd /dash/<daemon>/ route
// entries — no registry, no autodiscovery."
//
// The PROBE never gates the link. An unreachable daemon is exactly the one an
// operator wants to click through to diagnose; hiding the link on a bad probe
// would be backwards. Status is shown by the dot.
func shouldLink(routed []string, s service) bool {
	return slices.Contains(routed, s.Dash)
}

// routedDashPaths asks proxyd which /dash/ routes it carries.
//
// Over proxyd's own /v1, not the DB: that is the read path 6/28 requires of a
// cross-cutting dashd page, and the live table is the only reader that knows
// BOTH registration paths 6/28 names — core routes and the per-package
// manifests every channel adapter ships.
//
// On any failure it degrades to the core routes compiled in. That is honest
// rather than accidental: the core daemons keep their links, adapter tiles read
// "coming soon" until proxyd answers again, and nothing ever links at a path
// that does not exist. The alternative — assuming everything is routed — would
// send an operator to a 404 during precisely the incident when proxyd is the
// thing that is broken.
func (d *dash) routedDashPaths(ctx context.Context) []string {
	if d.proxydURL == "" {
		return compose.ReservedRoutePaths()
	}
	body, status, err := d.bearerCall(ctx, d.proxydURL, http.MethodGet, "/v1/proxyd_routes", nil)
	if err != nil || status < 200 || status >= 300 {
		slog.Warn("hub: proxyd routes unreadable; linking core daemons only",
			"status", status, "err", err)
		return compose.ReservedRoutePaths()
	}
	var routes []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(body, &routes); err != nil {
		slog.Warn("hub: proxyd routes did not decode; linking core daemons only", "err", err)
		return compose.ReservedRoutePaths()
	}
	out := make([]string, 0, len(routes))
	for _, rt := range routes {
		out = append(out, rt.Path)
	}
	return out
}

// handleServices renders the cockpit services hub: one tile per known daemon
// with a live /health status dot. Tiles with Built=true link into their /dash/
// surface regardless of probe status; others show the name as plain text
// until that surface ships.
func (d *dash) handleServices(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "Services")

	// One route read per render, shared by every tile: the hub asks proxyd once,
	// not once per daemon.
	routed := d.routedDashPaths(r.Context())

	statuses := make([]string, len(services))
	var wg sync.WaitGroup
	for i := range services {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = probeHealth(services[i].Host, services[i].Probe)
		}(i)
	}
	wg.Wait()

	counts := map[string]int{}
	for _, s := range statuses {
		counts[s]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	summary := ""
	for _, k := range keys {
		if summary != "" {
			summary += ", "
		}
		summary += fmt.Sprintf("%d %s", counts[k], k)
	}
	_, _ = fmt.Fprintf(w, `<p class="dim">Daemon health (%s). Control planes with a link are available; others are in progress.</p>`, esc(summary))

	_, _ = fmt.Fprint(w, `<div class="services-grid">`)
	for i, s := range services {
		// One question, one answer: a routed dashboard is a link, an unrouted
		// one says so. Rendering the link and the label from two different
		// facts is how tiles came to link at pages that were not there.
		nameHTML := esc(s.Name)
		if shouldLink(routed, s) {
			nameHTML = fmt.Sprintf(`<a href="%s">%s</a>`, esc(s.Dash), esc(s.Name))
		} else {
			nameHTML += ` <span class="dim">(coming soon)</span>`
		}
		_, _ = fmt.Fprintf(w,
			`<div class="service-tile" data-status="%s">`+
				`<h3><span class="dot dot-%s"></span><span class="status-%s">%s</span> %s</h3>`+
				`<p class="dim">%s</p>`+
				`</div>`,
			esc(statuses[i]),
			esc(statuses[i]),
			esc(statuses[i]), statusGlyph(statuses[i]),
			nameHTML,
			esc(s.Desc),
		)
	}
	_, _ = fmt.Fprint(w, `</div>`)

	if allUnknown(statuses) {
		_, _ = fmt.Fprint(w, `<p class="dim">Local dev: daemon DNS names don't resolve outside Docker network.</p>`)
	}

	pageClose(w, r)
}

// allUnknown reports whether every probe came back statusUnknown — the
// local-dev signature (daemon DNS names unresolvable outside the Docker net).
func allUnknown(statuses []string) bool {
	if len(statuses) == 0 {
		return false
	}
	for _, s := range statuses {
		if s != statusUnknown {
			return false
		}
	}
	return true
}

// statusGlyph maps a status to its dot glyph. The CSS color class is the status
// string itself (.status-ok/.status-err/.status-unknown), set at the call site.
func statusGlyph(status string) string {
	switch status {
	case statusOK:
		return "✓"
	case statusErr:
		return "✗"
	default:
		return "?"
	}
}
