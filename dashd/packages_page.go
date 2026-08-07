package main

import (
	"fmt"
	"net/http"
)

// handlePackages lists the instance's installed packages (spec 5/28): the
// installed_packages record, read from routd.db. Read-only, and so is the
// resource behind it (`GET /v1/installed_packages`, routd/packages_resource.go)
// — install / upgrade / remove is the `arizuko packages` CLI, because it also
// writes host files and restarts sidecars. There is no control surface to add
// here until there is one to call.
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
	// folder '' is instance-wide, a non-empty one names the group a product was
	// blended into (spec 5/28 composition). Without the column the two read as
	// the same fact, and a product looks like it installed a sidecar.
	rows, err := db.Query(`SELECT folder, name, source, revision, installed_at
		FROM installed_packages ORDER BY folder, name`)
	if err != nil {
		fmt.Fprint(w, htmlBanner("err", "read installed_packages: "+esc(err.Error())))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	fmt.Fprint(w, `<table class="tbl"><thead><tr><th>folder</th><th>name</th><th>source</th><th>revision</th><th>installed</th></tr></thead><tbody>`)
	n := 0
	for rows.Next() {
		var folder, name, source, revision, at string
		if err := rows.Scan(&folder, &name, &source, &revision, &at); err != nil {
			continue
		}
		n++
		scope := `<span class="dim">instance</span>`
		if folder != "" {
			scope = "<code>" + esc(folder) + "</code>"
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td><code>%s</code></td><td>%s</td></tr>`,
			scope, esc(name), esc(source), esc(revision), esc(at))
	}
	fmt.Fprint(w, `</tbody></table>`)
	if n == 0 {
		fmt.Fprint(w, `<p class="dim">No packages installed. Install one with `+
			`<code>arizuko packages &lt;instance&gt; install &lt;source&gt;</code>, `+
			`or blend a group's products with <code>arizuko products &lt;instance&gt; apply &lt;folder&gt;</code>.</p>`)
	}
	pageClose(w, r)
}
