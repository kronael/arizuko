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
		fmt.Fprint(w, htmlBanner("err", "no identity — sign in via proxyd to view your profile"))
		pageClose(w, r)
		return
	}
	if d.adminDB() == nil {
		fmt.Fprint(w, htmlBanner("err", "backend unavailable"))
		pageClose(w, r)
		return
	}

	var name string
	_ = d.adminDB().QueryRow(
		`SELECT name FROM user_profiles WHERE sub = ?`, sub).Scan(&name)
	fmt.Fprint(w, `<p class="dim">Your account and the providers you can add to it.</p>`)
	identity := `<table>` + htmlDetail("Your account ID", `<code>`+esc(sub)+`</code>`)
	if name != "" {
		identity += htmlDetail("Name", esc(name))
	}
	fmt.Fprint(w, identity+`</table>`)

	prefixes := map[string]bool{providerPrefix(sub): true}

	var providerLinks string
	for _, p := range supportedProviders {
		if prefixes[p.prefix] {
			continue
		}
		providerLinks += fmt.Sprintf(
			`<a class="oauth-btn" href="%s?intent=link&return=%s">Link %s</a>`,
			esc(p.path), esc("/dash/profile/"), esc(p.label))
	}
	if providerLinks == "" {
		providerLinks = `<p class="empty">All known providers already linked.</p>`
	} else {
		providerLinks = `<div class="form-narrow">` + providerLinks + `</div>`
	}
	fmt.Fprint(w, htmlSection("Add a provider", providerLinks))

	fmt.Fprint(w, htmlSection("API keys",
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
