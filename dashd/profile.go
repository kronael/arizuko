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
	pageTopFor(w, r, "Profile")
	if sub == "" {
		fmt.Fprint(w, htmlBanner("err", "no identity — sign in via proxyd to view your profile"))
		pageClose(w, r)
		return
	}

	var name string
	_ = d.db.QueryRow(
		`SELECT name FROM auth_users WHERE sub = ?`, sub).Scan(&name)
	fmt.Fprint(w, `<p class="dim">Your canonical identity and linked providers.</p>`)
	identity := `<table>` + htmlDetail("Canonical sub", `<code>`+esc(sub)+`</code>`)
	if name != "" {
		identity += htmlDetail("Name", esc(name))
	}
	fmt.Fprint(w, identity+`</table>`)

	linked, _ := d.linkedSubs(sub)
	prefixes := map[string]bool{providerPrefix(sub): true}
	var linkedRows [][]string
	for _, ls := range linked {
		linkedRows = append(linkedRows, []string{fmt.Sprintf(`<code>%s</code>`, esc(ls))})
		prefixes[providerPrefix(ls)] = true
	}
	fmt.Fprint(w, htmlSection("Linked accounts", htmlTable([]string{"Sub"}, linkedRows)))

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
		providerLinks = `<div style="max-width:420px">` + providerLinks + `</div>`
	}
	fmt.Fprint(w, htmlSection("Add a provider", providerLinks))

	pageClose(w, r)
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
