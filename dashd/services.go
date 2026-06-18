package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"sync"
	"time"
)

// service describes one daemon tile on the cockpit services hub. host is the
// Docker-network name probed at <host>:8080/health. Built is true when the
// /Dash route is deployed; false = control plane in progress.
type service struct {
	Name  string
	Host  string
	Dash  string
	Desc  string
	Built bool
}

// services is the static daemon list the hub probes. No autodiscovery — route
// entry IS registration (spec 6/1); adding a daemon here is a deliberate edit.
// Set Built=true once the per-daemon /dash/ route is shipped.
var services = []service{
	{"routd", "routd", "/dash/routd/", "message router, route table, breakers", true},
	{"runed", "runed", "/dash/runed/", "agent container runs and tokens", true},
	{"authd", "authd", "/dash/authd/", "identity keys, tokens, providers", false},
	{"proxyd", "proxyd", "/dash/proxyd/", "auth-gated reverse proxy", false},
	{"onbod", "onbod", "/dash/onbod/", "onboarding queue, gates, invites", true},
	{"timed", "timed", "/dash/timed/", "scheduled tasks and ticks", true},
	{"webd", "webd", "/dash/webd/", "web chat widget and routes", false},
	{"davd", "davd", "/dash/davd/", "WebDAV workspace access", false},
}

// healthTimeout caps each /health probe. The grid probes all daemons
// concurrently, so the whole page waits at most this long.
const healthTimeout = 500 * time.Millisecond

// statusOK/Err/Unknown classify a tile's status dot. ok = /health 2xx;
// err = reachable-but-unhealthy OR refused/timeout (daemon down in production);
// unknown = DNS failure (container not deployed or local-dev name mismatch).
const (
	statusOK      = "ok"
	statusErr     = "err"
	statusUnknown = "unknown"
)

// probeHealth GETs http://<host>:8080/health with a short timeout and maps the
// outcome to an ok/err/unknown status.
func probeHealth(host string) string {
	c := &http.Client{Timeout: healthTimeout}
	resp, err := c.Get("http://" + host + ":8080/health")
	return classifyHealth(resp, err)
}

// classifyHealth maps a /health GET outcome to a status.
// DNS failure or client timeout (host unreachable in ≤500ms) = unknown.
// Connection refused = deployed but daemon down = err.
func classifyHealth(resp *http.Response, err error) string {
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			return statusOK
		}
		return statusErr
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return statusUnknown
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return statusUnknown
	}
	return statusErr
}

// handleServices renders the cockpit services hub: one tile per known daemon
// with a live /health status dot. Tiles with Built=true link into their /dash/
// surface; others show the name as plain text until that surface ships.
func (d *dash) handleServices(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Services")

	statuses := make([]string, len(services))
	var wg sync.WaitGroup
	for i := range services {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			statuses[i] = probeHealth(services[i].Host)
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
	fmt.Fprintf(w, `<p class="dim">Daemon health (%s). Control planes with a link are available; others are in progress.</p>`, esc(summary))

	fmt.Fprint(w, `<div class="services-grid">`)
	for i, s := range services {
		var nameHTML string
		if s.Built && statuses[i] != statusUnknown {
			nameHTML = fmt.Sprintf(`<a href="%s">%s</a>`, esc(s.Dash), esc(s.Name))
		} else {
			nameHTML = esc(s.Name)
		}
		fmt.Fprintf(w,
			`<div class="service-tile" data-status="%s">`+
				`<h3><span class="status-%s">%s</span> %s</h3>`+
				`<p class="desc">%s</p>`+
				`</div>`,
			esc(statuses[i]),
			esc(statuses[i]), statusGlyph(statuses[i]),
			nameHTML,
			esc(s.Desc),
		)
	}
	fmt.Fprint(w, `</div>`)

	pageClose(w, r)
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
