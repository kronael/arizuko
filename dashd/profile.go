package main

import (
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/onvos/arizuko/theme"
)

// supportedProviders mirrors auth/routes.go — keep in sync. Each entry is
// {sub-prefix, label, redirect-path}. The redirect-path is what the
// "Link account" button points at; proxyd handles auth/{provider}.
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

// handleProfile renders /dash/profile. Reads the caller's canonical sub
// from X-User-Sub (proxyd-stamped, already canonical via the JWT mint
// resolve point) and lists currently-linked subs. For each provider not
// already linked, render a "Link account" button.
func (d *dash) handleProfile(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	sub := strings.TrimSpace(r.Header.Get("X-User-Sub"))
	pageTop(w, "Profile")
	if sub == "" {
		fmt.Fprint(w, `<p class="banner-err">no identity</p>`)
		fmt.Fprint(w, pageBot)
		return
	}

	var name string
	_ = d.db.QueryRow(
		`SELECT name FROM auth_users WHERE sub = ?`, sub).Scan(&name)
	fmt.Fprintf(w, `<p><b>Canonical sub:</b> <code>%s</code>`, esc(sub))
	if name != "" {
		fmt.Fprintf(w, ` &middot; %s`, esc(name))
	}
	fmt.Fprint(w, `</p>`)

	linked, _ := d.linkedSubs(sub)
	fmt.Fprint(w, `<h2>Linked accounts</h2>`)
	prefixes := map[string]bool{providerPrefix(sub): true}
	if len(linked) == 0 {
		fmt.Fprint(w, `<p><em>No additional providers linked.</em></p>`)
	} else {
		fmt.Fprint(w, `<ul>`)
		for _, ls := range linked {
			fmt.Fprintf(w, `<li><code>%s</code></li>`, esc(ls))
			prefixes[providerPrefix(ls)] = true
		}
		fmt.Fprint(w, `</ul>`)
	}

	fmt.Fprint(w, `<h2>Add a provider</h2>`)
	any := false
	for _, p := range supportedProviders {
		if prefixes[p.prefix] {
			continue
		}
		any = true
		fmt.Fprintf(w,
			`<a class="oauth-btn" href="%s?intent=link&return=%s">Link %s</a> `,
			esc(p.path), esc("/dash/profile/"), esc(p.label))
	}
	if !any {
		fmt.Fprint(w, `<p><em>All known providers already linked.</em></p>`)
	}

	// Render the same theme.css OAuth button style as /auth/login.
	fmt.Fprintf(w, `<style>%s</style>`, theme.CSS)

	fmt.Fprint(w, pageBot)
}

func (d *dash) linkedSubs(canonical string) ([]string, error) {
	rows, err := d.db.Query(
		`SELECT sub FROM auth_users WHERE linked_to_sub = ? ORDER BY sub`,
		canonical)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s.Valid && s.String != "" {
			out = append(out, s.String)
		}
	}
	return out, rows.Err()
}

func providerPrefix(sub string) string {
	if i := strings.Index(sub, ":"); i >= 0 {
		return sub[:i+1]
	}
	return ""
}
