package main

// dash.go is ttsd's operator dashboard at GET /dash/ttsd/ (spec 6/28, 6/30).
//
// Read-only, and 6/30 says so: backend selection is TTS_BACKEND_URL, an
// env-and-restart concern, so there is no control to offer. What an operator
// needs here is the one thing /health reduces to a status code — WHICH backend,
// whether it answered, and how long it took.

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/theme"
)

// dashHandler renders the backend's identity and a live probe. The probe runs
// per request rather than from a cached loop: this page is opened when someone
// suspects the backend is down, and a cached "ok" from a minute ago is the
// answer they must not get.
//
// ks is the authd verifier, and the gate is the one every other daemon calls:
// the stamped operator scope counts only behind proxyd's service:proxyd bearer.
// A nil ks is local dev — no authd, nothing stamping, nothing to enforce — and
// it falls open exactly as every other nil-KeySet gate in the tree does
// (6/28: daemon dashboards are operator-only).
func dashHandler(backend string, ks *auth.KeySet) http.HandlerFunc {
	client := &http.Client{Timeout: 3 * time.Second}
	return func(w http.ResponseWriter, r *http.Request) {
		if !auth.RequireOperator(w, r, ks, "ttsd") {
			return
		}
		start := time.Now()
		up := backendUp(client, backend)
		took := time.Since(start)

		var b strings.Builder
		fmt.Fprint(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › ttsd</div>`)
		fmt.Fprint(&b, `<h1>ttsd</h1>`)
		b.WriteString(backendPanel(backend, up, took))
		_, _ = fmt.Fprint(w, theme.Page("ttsd", template.HTML(b.String())))
	}
}

// backendPanel renders the probe result. The URL is shown because ttsd is a
// thin proxy — "the voice is broken" is almost always the wrong backend
// configured, and the operator cannot see TTS_BACKEND_URL from anywhere else.
func backendPanel(backend string, up bool, took time.Duration) string {
	state, word := "err", "unreachable"
	if up {
		state, word = "ok", "reachable"
	}
	return fmt.Sprintf(
		`<div class="tiles"><div class="tile %s"><div class="tile-n">%s</div>`+
			`<div class="tile-l">backend</div></div>`+
			`<div class="tile"><div class="tile-n">%d ms</div>`+
			`<div class="tile-l">probe</div></div></div>`+
			`<p>Backend: <code>%s</code></p>`+
			`<p class="dim">Selected by TTS_BACKEND_URL; changing it is an env edit and a restart.</p>`,
		html.EscapeString(state), html.EscapeString(word),
		took.Milliseconds(), html.EscapeString(backend))
}
