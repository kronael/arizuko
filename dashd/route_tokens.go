package main

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/kronael/arizuko/store"
)

// webhookLabelRe constrains webhook labels to URL-safe identifier chars so the
// composed hook: JID stays well-formed.
var webhookLabelRe = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

// Route tokens dashboard: per-folder list + issue + revoke.
// Mounted at:
//
//	GET  /dash/tokens/{folder}/     — folder-scoped list + issue form
//	POST /dash/tokens/{folder}/     — issue new token (kind in form)
//	POST /dash/tokens/{folder}/{jid}/revoke — revoke by JID

func (d *dash) handleTokensFolder(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	if r.Method == http.MethodPost {
		if _, ok := d.requireAdmin(w, r, folder); !ok {
			return
		}
	} else {
		if !d.requireVisible(w, r, folder) {
			return
		}
	}
	st := store.New(d.adminDB())

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Tokens — "+folder)

	if r.Method == http.MethodPost {
		if d.adminDB() == nil {
			fmt.Fprint(w, htmlBanner("err", "read-only mode"))
			pageClose(w, r)
			return
		}
		kind := r.FormValue("kind")
		label := r.FormValue("label")
		var jid string
		switch kind {
		case "chat":
			jid = "web:" + folder
		case "hook":
			if label == "" {
				fmt.Fprint(w, htmlBanner("err", "label required for webhook tokens"))
			} else if !webhookLabelRe.MatchString(label) {
				fmt.Fprint(w, htmlBanner("err", "invalid label: use letters, digits, . _ -"))
			} else {
				jid = "hook:" + folder + "/" + label
			}
		default:
			fmt.Fprint(w, htmlBanner("err", "unknown kind"))
		}
		if jid != "" {
			raw := store.GenRouteToken()
			var context any
			if c := r.FormValue("context"); c != "" {
				context = c
			}
			// Raw INSERT (not store.InsertRouteToken): routd.db has no audit_log
			// table, so the audited writer would roll back. Same audit-free
			// discipline as the secrets and grant rewires.
			_, err := d.adminDB().Exec(
				`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at, context, kind) VALUES (?, ?, ?, ?, ?, ?)`,
				store.HashRouteToken(raw), jid, folder, time.Now().Format(time.RFC3339Nano), context, store.RouteTokenKindRoute)
			if err != nil {
				fmt.Fprint(w, htmlBanner("err", "insert error: "+err.Error()))
			} else {
				fmt.Fprint(w, htmlBannerRaw("ok", "Token issued. Copy it now — it will not be shown again.<br><code>"+esc(raw)+"</code>"))
			}
		}
	}

	tokens := st.ListRouteTokens(folder)
	var tableRows [][]string
	for _, t := range tokens {
		kind := store.RouteTokenJIDKind(t.JID)
		revoke := fmt.Sprintf(
			`<form method="post" action="/dash/tokens/%s/%s/revoke">`+
				`<button class="btn btn-danger btn-sm" type="submit">revoke</button></form>`,
			folderPath(folder), esc(encodeJID(t.JID)))
		iso := t.CreatedAt.UTC().Format(time.RFC3339)
		tableRows = append(tableRows, []string{
			fmt.Sprintf(`<code>%s</code>`, esc(t.JID)),
			esc(kind),
			`<abbr title="` + esc(iso) + `">` + relativeTS(iso) + `</abbr>`,
			esc(t.Context),
			revoke,
		})
	}
	fmt.Fprint(w, htmlTable([]string{"JID", "Kind", "Created", "Context", ""}, tableRows,
		"No tokens. Issue a chat link or webhook token above."))

	fmt.Fprint(w, htmlSection("Issue new token",
		fmt.Sprintf(`<form method="post" action="/dash/tokens/%s/">`, folderPath(folder))+
			htmlFormRow("Kind", `<select name="kind">`+
				`<option value="chat">chat link</option>`+
				`<option value="hook">webhook</option>`+
				`</select>`)+
			htmlFormRow("Label (webhook only)", `<input name="label" type="text" placeholder="github">`)+
			htmlFormRow("Context (optional)", `<textarea name="context" rows="2" `+
				`placeholder="How the agent should handle this link's messages, e.g. bug reports; triage, don't chat"></textarea>`)+
			`<p><button type="submit" class="btn btn-primary">Issue</button></p>`+
			`</form>`))

	pageClose(w, r)
}

func (d *dash) handleTokensRevoke(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	// The {jid} segment was url.PathEscape'd into the revoke link; Go's mux
	// unescapes it back to the raw JID here (same as folderPath/{folder}).
	jid := r.PathValue("jid")

	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "read-only", http.StatusServiceUnavailable)
		return
	}
	// Raw DELETE (not store.RevokeRouteToken): audit-free for routd.db.
	res, err := d.adminDB().Exec(
		`DELETE FROM route_tokens WHERE jid = ? AND owner_folder = ? AND kind = ?`,
		jid, folder, store.RouteTokenKindRoute)
	if err != nil {
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dash/tokens/"+folderPath(folder)+"/", http.StatusSeeOther)
}

// encodeJID escapes a JID into a single URL path segment. url.PathEscape is
// reversible (Go's mux unescapes it back in PathValue), unlike the old "/"→"--"
// scheme, which collided with labels containing "--" (labels allow "-").
func encodeJID(jid string) string {
	return url.PathEscape(jid)
}
