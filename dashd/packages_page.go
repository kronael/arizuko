package main

import (
	"fmt"
	"net/http"
)

// handlePackages lists the instance's installed packages (spec 5/28): the
// installed_packages record, read from routd.db. Read-only for v1 — install /
// upgrade / remove is the `arizuko packages` CLI.
func (d *dash) handlePackages(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "packages")

	db := d.adminDB()
	if db == nil {
		fmt.Fprint(w, htmlBanner("err", "package store unavailable"))
		pageClose(w, r)
		return
	}
	rows, err := db.Query(`SELECT name, source, revision, installed_at
		FROM installed_packages ORDER BY name`)
	if err != nil {
		fmt.Fprint(w, htmlBanner("err", "read installed_packages: "+esc(err.Error())))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	fmt.Fprint(w, `<table class="tbl"><thead><tr><th>name</th><th>source</th><th>revision</th><th>installed</th></tr></thead><tbody>`)
	n := 0
	for rows.Next() {
		var name, source, revision, at string
		if err := rows.Scan(&name, &source, &revision, &at); err != nil {
			continue
		}
		n++
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td><code>%s</code></td><td>%s</td></tr>`,
			esc(name), esc(source), esc(revision), esc(at))
	}
	fmt.Fprint(w, `</tbody></table>`)
	if n == 0 {
		fmt.Fprint(w, `<p class="dim">No packages installed. Install one with `+
			`<code>arizuko packages &lt;instance&gt; install &lt;source&gt;</code>.</p>`)
	}
	pageClose(w, r)
}
