package main

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/kronael/arizuko/auth"
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

	// Tool visibility is auth.EffectiveActions over the folder's acl rows (4/R: the
	// same view the agent socket uses — a tool shows iff the folder holds it, reads
	// unconditional). No tier/DeriveRules.
	s := store.New(d.adminDB())
	held := auth.EffectiveActions(s, auth.Caller{Principal: "folder:" + folder})
	tools := ipc.ListTools(folder, func(name string) bool { return held("mcp:" + name) })

	fmt.Fprintf(w, `<p class="dim">%d tools available to <code>%s</code>. Read-only — modify via grants.</p>`,
		len(tools), esc(folder))

	for _, t := range tools {
		schemaJSON, _ := json.MarshalIndent(t.InputSchema, "", "  ")
		fmt.Fprintf(w, `<details class="tool-card">`+
			`<summary>%s</summary>`+
			`<p>%s</p>`+
			`<pre>%s</pre>`+
			`</details>`,
			esc(t.Name), esc(t.Description), esc(string(schemaJSON)))
	}

	pageClose(w, r)
}
