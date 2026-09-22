package chanlib

// dash.go is THE adapter dashboard (spec 6/31), implemented once and mounted
// by every Go adapter through NewAdapterMux: one renderer with per-adapter
// deltas, never ten copies of a shared grammar — every fact the page shows is
// already a parameter of NewAdapterMux. The two TypeScript adapters (whapd,
// twitd) re-implement the contract in their own source, which is why 6/31
// writes it down rather than leaving it implied by this file.

import (
	"fmt"
	"html"
	"html/template"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/theme"
)

// DashSection is one per-adapter delta panel (spec 6/32): an adapter with
// state the others do not have contributes a section rather than a second
// page. Rows are label/value pairs, because giving adapters HTML would let ten
// pages drift into ten layouts; an adapter that needs more extends this type.
type DashSection struct {
	Title string
	Rows  [][2]string
}

// DashSectioner is the OPTIONAL interface an adapter implements to add delta
// sections; an adapter that does not gets the shared page and nothing else.
type DashSectioner interface {
	DashSections() []DashSection
}

// handleAdapterDash renders the single adapter page 6/31 specifies: overview,
// connection, stream health, capabilities, then any per-adapter deltas. ks is
// injected because AuthdKeys is built once from the process env, so a test
// cannot swap it.
func handleAdapterDash(name string, prefixes []string, bot BotHandler,
	isConnected func() bool, lastInboundAt func() int64, ks *auth.KeySet) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// X-User-* is trustworthy only behind proxyd's service:proxyd bearer;
		// reading the scope alone let anyone inside the compose network send
		// `X-User-Groups: ["**"]` and receive the operator page (BUGS L270).
		if !auth.RequireOperator(w, r, ks, name) {
			return
		}
		connected := isConnected()
		last := lastInboundAt()

		var b strings.Builder
		fmt.Fprintf(&b, `<div class="crumb"><a href="/dash/services/">arizuko hub</a> › %s</div>`,
			html.EscapeString(name))
		fmt.Fprintf(&b, `<h1>%s</h1>`, html.EscapeString(name))
		b.WriteString(adapterOverview(name, prefixes, connected, last))
		b.WriteString(capStrip(bot))
		if ds, ok := bot.(DashSectioner); ok {
			for _, sec := range ds.DashSections() {
				b.WriteString(renderSection(sec))
			}
		}
		fmt.Fprint(w, theme.Page(name, template.HTML(b.String())))
	}
}

// adapterOverview renders the status tiles. Status has THREE values: `stale`
// is a live process whose platform link has gone quiet — the runbook's
// "healthchecks green but the agent doesn't reply" case — and collapsing it
// into ok or disconnected loses the only signal that distinguishes them.
func adapterOverview(name string, prefixes []string, connected bool, last int64) string {
	status := "disconnected"
	switch {
	case connected && last > 0 && time.Since(time.Unix(last, 0)) > staleThresholdFor(name):
		status = "stale"
	case connected:
		status = "ok"
	}
	inbound := "never"
	if last > 0 {
		inbound = time.Unix(last, 0).UTC().Format(time.RFC3339)
	}
	return fmt.Sprintf(
		`<div class="tiles">`+
			`<div class="tile %s"><div class="tile-n">%s</div><div class="tile-l">status</div></div>`+
			`<div class="tile"><div class="tile-n">%s</div><div class="tile-l">last inbound</div></div>`+
			`</div><p>JID prefixes: <code>%s</code></p>`,
		html.EscapeString(tileState(status)), html.EscapeString(status),
		html.EscapeString(inbound), html.EscapeString(strings.Join(prefixes, ", ")))
}

// tileState maps the three statuses onto the theme's two visual states. A
// stale adapter warns rather than errs: the operator's next step is the
// platform link, not a restart.
func tileState(status string) string {
	switch status {
	case "ok":
		return "ok"
	case "stale":
		return "warn"
	default:
		return "err"
	}
}

// staleThresholdFor returns the adapter's staleness window, falling back to
// /health's own default so the page and /health cannot disagree.
func staleThresholdFor(name string) time.Duration {
	if t, ok := staleThresholds[name]; ok {
		return t
	}
	return defaultStaleThreshold
}

// capStrip renders which verbs this adapter really implements, PROBED rather
// than declared: a cap list read from config is a claim, and isStubVerb is the
// same check CapImplReport uses to catch drift.
func capStrip(bot BotHandler) string {
	if bot == nil {
		return ""
	}
	names := make([]string, 0, len(gatedVerbProbes))
	for cap := range gatedVerbProbes {
		names = append(names, cap)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString(`<h2>Capabilities</h2><p class="dim">Probed from the verb, not read from config.</p><p>`)
	for _, cap := range names {
		cls := "dim"
		if !isStubVerb(bot, gatedVerbProbes[cap]) {
			cls = "ok"
		}
		fmt.Fprintf(&b, `<span class="%s">%s</span> `, cls, html.EscapeString(cap))
	}
	b.WriteString(`</p>`)
	return b.String()
}

// renderSection renders one delta panel. An empty section still renders its
// title with a clean-state line: omitting the panel would look identical to
// the adapter not having the feature.
func renderSection(sec DashSection) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<h2>%s</h2>`, html.EscapeString(sec.Title))
	if len(sec.Rows) == 0 {
		return b.String() + `<p class="dim">Nothing to show.</p>`
	}
	b.WriteString(`<table><tbody>`)
	for _, row := range sec.Rows {
		fmt.Fprintf(&b, `<tr><td>%s</td><td><code>%s</code></td></tr>`,
			html.EscapeString(row[0]), html.EscapeString(row[1]))
	}
	b.WriteString(`</tbody></table>`)
	return b.String()
}
