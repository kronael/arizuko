package main

import (
	"fmt"
	"net/http"
	"strings"
)

// mirrors auth/routes.go — keep in sync.
var supportedProviders = []struct {
	prefix string
	label  string
	path   string
}{
	{"google:", "Google", "/auth/google"},
	{"github:", "GitHub", "/auth/github"},
	{"discord:", "Discord", "/auth/discord"},
	{"telegram:", "Telegram", "/auth/login"},
}

func (d *dash) handleProfile(w http.ResponseWriter, r *http.Request) {
	sub := strings.TrimSpace(r.Header.Get("X-User-Sub"))
	pageTopFor(w, r, "Profile")
	if sub == "" {
		_, _ = fmt.Fprint(w, htmlBanner("err", "no identity — sign in via proxyd to view your profile"))
		pageClose(w, r)
		return
	}
	if d.routdStore() == nil {
		_, _ = fmt.Fprint(w, htmlBanner("err", "backend unavailable"))
		pageClose(w, r)
		return
	}

	// routd owns user_profiles; the page asks for the caller's display name
	// rather than reading the table (6/28, BUGS L142). A failure is silent by
	// design here — the name is decoration, and the page renders without it.
	var name string
	if ps, err := d.principalsViaRoutd(r.Context()); err == nil {
		for _, pr := range ps {
			if pr.Sub == sub {
				name = pr.Name
				break
			}
		}
	}
	_, _ = fmt.Fprint(w, `<p class="dim">Your account and the providers you can add to it.</p>`)
	identity := `<table>` + htmlDetail("Your account ID", `<code>`+esc(sub)+`</code>`)
	if name != "" {
		identity += htmlDetail("Name", esc(name))
	}
	_, _ = fmt.Fprint(w, identity+`</table>`)

	// One identity reaches this page: the caller's own sub, stamped by proxyd.
	// Which other providers resolve to the same account is authd's answer
	// (`GET /v1/identities/{sub}`) and `service:dashd` holds no `identity:read`
	// scope to ask for it, so the page offers every provider but the caller's
	// own and claims nothing about the rest. One prefix hidden out of four
	// leaves the list non-empty on every render, so there is no all-linked
	// state to write (BUGS P2).
	own := providerPrefix(sub)

	var providerLinks string
	for _, p := range supportedProviders {
		if p.prefix == own {
			continue
		}
		providerLinks += fmt.Sprintf(
			`<a class="oauth-btn" href="%s?intent=link&return=%s">Link %s</a>`,
			esc(p.path), esc("/dash/profile/"), esc(p.label))
	}
	_, _ = fmt.Fprint(w, htmlSection("Add a provider",
		`<div class="form-narrow">`+providerLinks+`</div>`))

	_, _ = fmt.Fprint(w, htmlSection("API keys",
		`<p class="dim">Bring your own API keys (e.g. ANTHROPIC_API_KEY). They override the group's keys when the agent runs for you.</p>`+
			`<p><a class="btn btn-secondary" href="/dash/me/secrets">Manage API keys</a></p>`))

	pageClose(w, r)
}

func providerPrefix(sub string) string {
	if i := strings.Index(sub, ":"); i >= 0 {
		return sub[:i+1]
	}
	return ""
}
