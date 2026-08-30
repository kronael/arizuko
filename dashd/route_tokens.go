package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"

	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// Route tokens dashboard: per-folder list + revoke. READ-AND-REVOKE ONLY —
// the cockpit mints nothing. Mounted at:
//
//	GET  /dash/tokens/{folder}/             — folder-scoped list
//	POST /dash/tokens/{folder}/{jid}/revoke — revoke by JID
//
// Minting lives with the resource owner: routd's route_tokens resource serves
// both faces (POST /v1/route_tokens/{chat,hook} for operators, issue_chat_link
// / issue_webhook / issue_pairing_link for agents), and `arizuko token` for the
// CLI. A mint form here was a second writer over the same table with its own
// label validation — the shape that drifts (BUGS V2, spec 7/3 §webd: "There is
// no token minting anywhere in the cockpit").

func (d *dash) handleTokensFolder(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	if !d.requireVisible(w, r, folder) {
		return
	}
	pageTopFor(w, r, "Tokens — "+folder)

	// READ through routd's /v1, not routd.db (spec 6/28 §"Read-path: /v1 only,
	// no direct DB"). routd owns route_tokens and already serves the same rows
	// to the agent and the CLI, so this page is a third face on one reader
	// rather than a second projection of the table.
	//
	// The revoke below still writes through the store, deliberately: it uses
	// Store.AsUser to put the OPERATOR on the audit row, and bearerCall carries
	// only dashd's service bearer, so moving the WRITE would quietly re-attribute
	// every revoke to service:dashd. That half waits on 6/29's identity decision.
	tokens, err := d.listRouteTokens(r.Context(), folder)
	if err != nil {
		slog.Warn("tokens page: list", "folder", folder, "err", err)
		fmt.Fprint(w, htmlBanner("err", "could not reach routd for token data"))
		pageClose(w, r)
		return
	}
	var tableRows [][]string
	for _, t := range tokens {
		kind := store.RouteTokenJIDKind(t.JID)
		revoke := fmt.Sprintf(
			`<form method="post" action="/dash/tokens/%s/%s/revoke">`+
				`<button class="btn btn-danger btn-sm" type="submit">revoke</button></form>`,
			folderPath(folder), esc(encodeJID(t.JID)))
		iso := t.CreatedAt
		tableRows = append(tableRows, []string{
			fmt.Sprintf(`<code>%s</code>`, esc(t.JID)),
			esc(kind),
			abbrTS(iso),
			esc(t.Context),
			revoke,
		})
	}
	fmt.Fprint(w, htmlTable([]string{"JID", "Kind", "Created", "Context", ""}, tableRows,
		"No tokens for this group."))

	fmt.Fprint(w, htmlSection("Issuing a token",
		`<p class="dim">Tokens are minted where they are owned, not here. `+
			`Ask the agent for a chat link or a webhook, `+
			`POST <code>/v1/route_tokens/chat</code> or <code>/v1/route_tokens/hook</code>, `+
			`or run <code>arizuko token</code>. This page lists and revokes.</p>`))

	pageClose(w, r)
}

func (d *dash) handleTokensRevoke(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	// The {jid} segment was url.PathEscape'd into the revoke link; Go's mux
	// unescapes it back to the raw JID here (same as folderPath/{folder}).
	jid := r.PathValue("jid")

	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "read-only", http.StatusServiceUnavailable)
		return
	}
	// RevokeRouteToken, the audited writer (routd.db has audit_log since routd
	// migration 0016). AsUser attributes the revoke to the operator.
	hit, err := store.New(d.adminDB()).AsUser(sub).RevokeRouteToken(jid, folder)
	if err != nil || !hit {
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

// listRouteTokens reads one folder's delivery tokens from routd's /v1 face.
//
// The row type is resources.RouteTokensRow — the SAME struct routd's handler
// serves and /openapi.json emits a schema from — so the page cannot drift into
// rendering a field the API does not have.
func (d *dash) listRouteTokens(ctx context.Context, folder string) ([]resources.RouteTokensRow, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet,
		"/v1/route_tokens?folder="+url.QueryEscape(folder), nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/route_tokens: status %d", status)
	}
	var rows []resources.RouteTokensRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
