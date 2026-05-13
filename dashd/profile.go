package main

import (
	"database/sql"
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	sub := strings.TrimSpace(r.Header.Get("X-User-Sub"))
	pageTop(w, "Profile")
	if sub == "" {
		fmt.Fprint(w, `<div class="banner-err">no identity — sign in via proxyd to view your profile</div>`)
		fmt.Fprint(w, pageBot)
		return
	}

	var name string
	_ = d.db.QueryRow(
		`SELECT name FROM auth_users WHERE sub = ?`, sub).Scan(&name)
	fmt.Fprint(w, `<p class="dim">Your canonical identity and linked providers.</p>`)
	fmt.Fprint(w, `<table><tr><th>Canonical sub</th><td><code>`+esc(sub)+`</code></td></tr>`)
	if name != "" {
		fmt.Fprintf(w, `<tr><th>Name</th><td>%s</td></tr>`, esc(name))
	}
	fmt.Fprint(w, `</table>`)

	linked, _ := d.linkedSubs(sub)
	fmt.Fprint(w, `<h2>Linked accounts</h2>`)
	prefixes := map[string]bool{providerPrefix(sub): true}
	if len(linked) == 0 {
		fmt.Fprint(w, `<p class="empty">No additional providers linked.</p>`)
	} else {
		fmt.Fprint(w, `<table><thead><tr><th>Sub</th></tr></thead><tbody>`)
		for _, ls := range linked {
			fmt.Fprintf(w, `<tr><td><code>%s</code></td></tr>`, esc(ls))
			prefixes[providerPrefix(ls)] = true
		}
		fmt.Fprint(w, `</tbody></table>`)
	}

	fmt.Fprint(w, `<h2>Add a provider</h2>`)
	any := false
	fmt.Fprint(w, `<div style="max-width:420px">`)
	for _, p := range supportedProviders {
		if prefixes[p.prefix] {
			continue
		}
		any = true
		fmt.Fprintf(w,
			`<a class="oauth-btn" href="%s?intent=link&return=%s">Link %s</a>`,
			esc(p.path), esc("/dash/profile/"), esc(p.label))
	}
	fmt.Fprint(w, `</div>`)
	if !any {
		fmt.Fprint(w, `<p class="empty">All known providers already linked.</p>`)
	}

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
