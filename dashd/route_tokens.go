package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"

	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
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
// label validation — the shape that drifts (BUGS V2,
// spec 6/30-per-daemon-dashboards §webd: "There
// is no token minting anywhere in the cockpit").

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
		_, _ = fmt.Fprint(w, htmlBanner("err", "could not reach routd for token data"))
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
	_, _ = fmt.Fprint(w, htmlTable([]string{"JID", "Kind", "Created", "Context", ""}, tableRows,
		"No tokens for this group."))

	_, _ = fmt.Fprint(w, htmlSection("Issuing a token",
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
	if d.routdStore() == nil {
		http.Error(w, "read-only", http.StatusServiceUnavailable)
		return
	}
	// RevokeRouteToken, the audited writer (routd.db has audit_log since routd
	// migration 0016). AsUser attributes the revoke to the operator.
	hit, err := d.routdStore().AsUser(sub).RevokeRouteToken(jid, folder)
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

// foldersViaRoutd lists every registered group folder through routd's /v1,
// which is what 6/28's read path requires: routd owns `groups`, so dashd asks
// rather than reads its file.
//
// One reader, four callers. `SELECT folder FROM groups ORDER BY folder LIMIT
// 500` was copy-pasted into the groups page, the memory page, the usage page
// and the proactive page — four projections of one table, each free to drift
// into a WHERE the owner would not serve (BUGS L142).
func (d *dash) foldersViaRoutd(ctx context.Context) ([]string, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, "/v1/groups", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/groups: status %d", status)
	}
	var rows []struct {
		Folder string `json:"folder"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	// routd serves rows in table order; the pages render a sorted list, and
	// sorting HERE keeps that a property of the reader rather than of four
	// callers that must each remember.
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Folder != "" {
			out = append(out, r.Folder)
		}
	}
	sort.Strings(out)
	return out, nil
}

// tasksViaRoutd lists every scheduled task through routd's /v1. dashd's service
// token carries an empty folder claim, which routd's tasksTarget reads as
// list-all; the row type is the one routd's handler serves, so the page cannot
// render a field the API does not have.
func (d *dash) tasksViaRoutd(ctx context.Context) ([]resources.ScheduledTasksRow, error) {
	// GET /v1/tasks, not /v1/<name>: the scheduled_tasks resource deliberately
	// serves a different REST path because timed calls it across the container
	// boundary (resources/scheduled_tasks.go, BUGS F21). Assuming the resource
	// name IS the path answered 404 on the first attempt.
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, "/v1/tasks", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/tasks: status %d", status)
	}
	var rows []resources.ScheduledTasksRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// SessionCount is one folder's live session tally, as routd's
// GET /v1/sessions/counts serves it.
type SessionCount struct {
	Folder string `json:"folder"`
	Count  int    `json:"count"`
}

// sessionCountsViaRoutd reads the Status page's per-folder session tally from
// the daemon that owns `sessions` (6/28, BUGS L142).
func (d *dash) sessionCountsViaRoutd(ctx context.Context) ([]SessionCount, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, "/v1/sessions/counts", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/sessions/counts: status %d", status)
	}
	var rows []SessionCount
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// DailyVolume is one day's human-message count, as routd's
// GET /v1/messages/volume serves it. The is_bot_message filter lives with the
// owner, so this page cannot widen it to include the agent's own output.
type DailyVolume struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

func (d *dash) messageVolumeViaRoutd(ctx context.Context) ([]DailyVolume, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, "/v1/messages/volume", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/messages/volume: status %d", status)
	}
	var rows []DailyVolume
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// ActivityRow is one line of the cross-folder activity feed, as routd's
// GET /v1/messages/activity serves it. Preview is truncated by the owner.
type ActivityRow struct {
	Timestamp string `json:"timestamp"`
	Source    string `json:"source"`
	ChatJID   string `json:"chat_jid"`
	Sender    string `json:"sender"`
	Verb      string `json:"verb"`
	Preview   string `json:"preview"`
}

func (d *dash) activityViaRoutd(ctx context.Context, before string, limit int) ([]ActivityRow, error) {
	path := fmt.Sprintf("/v1/messages/activity?limit=%d", limit)
	if before != "" {
		path += "&before=" + url.QueryEscape(before)
	}
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/messages/activity: status %d", status)
	}
	var rows []ActivityRow
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// Principal is one grants-form datalist entry: a sub a grant may name, with an
// optional display name. Sub and name only — the form needs a label and an id.
type Principal struct {
	Sub  string `json:"sub"`
	Name string `json:"name"`
}

func (d *dash) principalsViaRoutd(ctx context.Context) ([]Principal, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, "/v1/principals", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/principals: status %d", status)
	}
	var rows []Principal
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}

// answeringViaRoutd reads whether agents are answering people from the daemon
// that owns `messages` and `turn_context` (6/28). It is the whole
// GET /v1/routing/answering envelope, because the two halves are one question
// and the page shows them together.
//
// It shares routd's apiv1 struct rather than re-declaring a twin, so the page
// cannot drift into rendering a field the API does not serve.
func (d *dash) answeringViaRoutd(ctx context.Context) (apiv1.AnsweringResponse, error) {
	var out apiv1.AnsweringResponse
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, "/v1/routing/answering", nil)
	if err != nil {
		return out, err
	}
	if status < 200 || status >= 300 {
		return out, fmt.Errorf("routd /v1/routing/answering: status %d", status)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	return out, nil
}
