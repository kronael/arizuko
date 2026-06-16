package main

import (
	"fmt"
	"net/http"
	"sort"
	"sync"
	"time"
)

// service describes one daemon tile on the cockpit services hub. host is the
// Docker-network name probed at <host>:8080/health; dash is the daemon's own
// /dash/ surface (rendered as a link even before that surface exists).
type service struct {
	Name string
	Host string
	Dash string
	Desc string
}

// services is the static daemon list the hub probes. No autodiscovery — route
// entry IS registration (spec 6/1); adding a daemon here is a deliberate edit.
var services = []service{
	{"routd", "routd", "/dash/routd/", "message router, route table, breakers"},
	{"runed", "runed", "/dash/runed/", "agent container runs and tokens"},
	{"authd", "authd", "/dash/authd/", "identity keys, tokens, providers"},
	{"proxyd", "proxyd", "/dash/proxyd/", "auth-gated reverse proxy"},
	{"onbod", "onbod", "/dash/onbod/", "onboarding queue, gates, invites"},
	{"timed", "timed", "/dash/timed/", "scheduled tasks and ticks"},
	{"webd", "webd", "/dash/webd/", "web chat widget and routes"},
	{"davd", "davd", "/dash/davd/", "WebDAV workspace access"},
}

// healthTimeout caps each /health probe. The grid probes all daemons
// concurrently, so the whole page waits at most this long.
const healthTimeout = 500 * time.Millisecond

// statusOK/Err/Unknown classify a tile's status dot. ok = /health 2xx;
// err = reachable but unhealthy; unknown = unreachable (local dev, where
// daemon hostnames don't resolve, or the daemon is down).
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

// classifyHealth maps a /health GET outcome to a status. A dial/timeout error
// (unreachable host) is "unknown", not "err" — in local dev the daemon names
// don't resolve and that is expected, not a failure to surface red. A reachable
// daemon returning non-2xx is "err".
func classifyHealth(resp *http.Response, err error) string {
	if err != nil {
		return statusUnknown
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return statusOK
	}
	return statusErr
}

// handleServices renders the cockpit services hub: one tile per known daemon
// with a live /health status dot and a link into its /dash/ surface. Read-only,
// operator-gated. Probes run concurrently so the page waits one timeout, not N.
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
	fmt.Fprintf(w, `<p class="dim">Daemon health across the instance (%s). Click a tile for its control plane.</p>`, esc(summary))

	fmt.Fprint(w, `<div class="services-grid">`)
	for i, s := range services {
		fmt.Fprintf(w,
			`<div class="service-tile" data-status="%s">`+
				`<h3><span class="status-%s">%s</span> <a href="%s">%s</a></h3>`+
				`<p class="desc">%s</p>`+
				`</div>`,
			esc(statuses[i]),
			esc(statuses[i]), statusGlyph(statuses[i]),
			esc(s.Dash), esc(s.Name),
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
