package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/store"
)

// Route tokens dashboard: per-folder list + issue + revoke.
// Mounted at:
//
//	GET  /dash/tokens/              — list all tokens (operator-wide)
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
		if _, ok := requireUser(w, r); !ok {
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
			} else {
				jid = "hook:" + folder + "/" + label
			}
		default:
			fmt.Fprint(w, htmlBanner("err", "unknown kind"))
		}
		if jid != "" {
			raw := store.GenRouteToken()
			// Raw INSERT (not store.InsertRouteToken): routd.db has no audit_log
			// table, so the audited writer would roll back. Same audit-free
			// discipline as the secrets and grant rewires.
			_, err := d.adminDB().Exec(
				`INSERT INTO route_tokens (token_hash, jid, owner_folder, created_at) VALUES (?, ?, ?, ?)`,
				store.HashRouteToken(raw), jid, folder, time.Now().Format(time.RFC3339Nano))
			if err != nil {
				fmt.Fprint(w, htmlBanner("err", "insert error: "+err.Error()))
			} else {
				fmt.Fprint(w, htmlBanner("ok", "Token issued. Copy it now — it will not be shown again.<br><code>"+esc(raw)+"</code>"))
			}
		}
	}

	tokens := st.ListRouteTokens(folder)
	var tableRows [][]string
	for _, t := range tokens {
		kind := store.RouteTokenKind(t.JID)
		revoke := fmt.Sprintf(
			`<form method="post" action="/dash/tokens/%s/%s/revoke">`+
				`<button class="btn btn-danger btn-sm" type="submit">revoke</button></form>`,
			folderPath(folder), esc(encodeJID(t.JID)))
		tableRows = append(tableRows, []string{
			fmt.Sprintf(`<code>%s</code>`, esc(t.JID)),
			esc(kind),
			t.CreatedAt.UTC().Format("2006-01-02 15:04"),
			revoke,
		})
	}
	fmt.Fprint(w, htmlTable([]string{"JID", "Kind", "Created", ""}, tableRows))

	fmt.Fprint(w, htmlSection("Issue new token",
		fmt.Sprintf(`<form method="post" action="/dash/tokens/%s/">`, folderPath(folder))+
			htmlFormRow("Kind", `<select name="kind">`+
				`<option value="chat">chat link</option>`+
				`<option value="hook">webhook</option>`+
				`</select>`)+
			htmlFormRow("Label (webhook only)", `<input name="label" type="text" placeholder="github">`)+
			`<p><button type="submit" class="btn btn-primary">Issue</button></p>`+
			`</form>`))

	pageClose(w, r)
}

func (d *dash) handleTokensRevoke(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	jidEnc := r.PathValue("jid")
	jid := decodeJID(jidEnc)

	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "read-only", http.StatusServiceUnavailable)
		return
	}
	// Raw DELETE (not store.RevokeRouteToken): audit-free for routd.db.
	res, err := d.adminDB().Exec(
		`DELETE FROM route_tokens WHERE jid = ? AND owner_folder = ?`, jid, folder)
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

// encodeJID replaces / with - for use in URL path segments.
func encodeJID(jid string) string {
	return strings.ReplaceAll(jid, "/", "--")
}

func decodeJID(enc string) string {
	return strings.ReplaceAll(enc, "--", "/")
}
