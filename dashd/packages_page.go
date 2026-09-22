package main

import (
	"fmt"
	"net/http"
)

// handlePackages lists the instance's installed packages (spec 5/28-packages): the
// installed_packages record, read from routd.db. Read-only, and so is the
// resource behind it (`GET /v1/installed_packages`, routd/packages_resource.go)
// — install / upgrade / remove is the `arizuko packages` CLI, because it also
// writes host files and restarts sidecars. There is no control surface to add
// here until there is one to call.
func (d *dash) handlePackages(w http.ResponseWriter, r *http.Request) {
	if !d.requireOperator(w, r) {
		return
	}
	pageTopFor(w, r, "packages")

	st := d.ds()
	if st == nil {
		fmt.Fprint(w, htmlBanner("err", "package store unavailable"))
		pageClose(w, r)
		return
	}
	pkgs, err := st.InstalledPackages()
	if err != nil {
		fmt.Fprint(w, htmlBanner("err", "read installed_packages: "+esc(err.Error())))
		pageClose(w, r)
		return
	}

	fmt.Fprint(w, `<table class="tbl"><thead><tr><th>folder</th><th>name</th><th>source</th><th>revision</th><th>installed</th></tr></thead><tbody>`)
	for _, p := range pkgs {
		scope := `<span class="dim">instance</span>`
		if p.Folder != "" {
			scope = "<code>" + esc(p.Folder) + "</code>"
		}
		fmt.Fprintf(w, `<tr><td>%s</td><td>%s</td><td>%s</td><td><code>%s</code></td><td>%s</td></tr>`,
			scope, esc(p.Name), esc(p.Source), esc(p.Revision), esc(p.InstalledAt))
	}
	fmt.Fprint(w, `</tbody></table>`)
	if len(pkgs) == 0 {
		fmt.Fprint(w, `<p class="dim">No packages installed. Install one with `+
			`<code>arizuko packages &lt;instance&gt; install &lt;source&gt;</code>, `+
			`or blend a group's products with <code>arizuko products &lt;instance&gt; apply &lt;folder&gt;</code>.</p>`)
	}
	pageClose(w, r)
}
