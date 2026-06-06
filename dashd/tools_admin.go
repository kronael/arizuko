package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/grants"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

// GET /dash/groups/{folder}/tools — read-only MCP tool browser.
// Shows the tool list the agent in this group sees: name, description, input
// schema. Source of truth is ipc.ListTools (same path gated uses at runtime).
func (d *dash) handleGroupTools(w http.ResponseWriter, r *http.Request) {
	folder := groupFromPath(r, "/tools")
	if folder == "" {
		http.Error(w, "bad folder", http.StatusBadRequest)
		return
	}
	if !d.requireVisible(w, r, folder) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Tools — "+folder,
		struct{ Href, Label string }{"/dash/groups/", "Groups"},
		struct{ Href, Label string }{"", folder},
		struct{ Href, Label string }{"", "Tools"},
	)

	id := auth.Resolve(folder)
	s := store.New(d.adminDB())
	rules := grants.DeriveRules(s, folder, id.Tier, auth.WorldOf(folder))
	tools := ipc.ListTools(folder, rules)

	fmt.Fprintf(w, `<p class="dim">%d tools available to <code>%s</code> (tier %d). Read-only — modify via grants or tier.</p>`,
		len(tools), esc(folder), id.Tier)

	for _, t := range tools {
		schemaJSON, _ := json.MarshalIndent(t.InputSchema, "", "  ")
		// Strip the grants: suffix from description for display.
		desc := t.Description
		if idx := strings.Index(desc, "\ngrants:"); idx != -1 {
			desc = desc[:idx]
		}
		fmt.Fprintf(w, `<details class="tool-card">`+
			`<summary>%s</summary>`+
			`<p>%s</p>`+
			`<pre>%s</pre>`+
			`</details>`,
			esc(t.Name), esc(desc), esc(string(schemaJSON)))
	}

	pageClose(w, r)
}
