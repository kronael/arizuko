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

func (d *dash) handleTokensFolder(w http.ResponseWriter, r *http.Request) {
	folder := r.PathValue("folder")
	if !d.requireVisible(w, r, folder) {
		return
	}
	pageTopFor(w, r, "Tokens — "+folder)

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

	jid := r.PathValue("jid")

	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.routdStore() == nil {
		http.Error(w, "read-only", http.StatusServiceUnavailable)
		return
	}

	hit, err := d.routdStore().AsUser(sub).RevokeRouteToken(jid, folder)
	if err != nil || !hit {
		http.Error(w, "revoke failed", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/dash/tokens/"+folderPath(folder)+"/", http.StatusSeeOther)
}

func encodeJID(jid string) string {
	return url.PathEscape(jid)
}

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

	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.Folder != "" {
			out = append(out, r.Folder)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (d *dash) tasksViaRoutd(ctx context.Context) ([]resources.ScheduledTasksRow, error) {
	if d.routd == nil {
		return nil, fmt.Errorf("routd store unavailable")
	}
	return d.taskSnapshot.Read(ctx, d.routd, store.OwnerScope(store.OwnerRoutd), d.loadTasks)
}
func (d *dash) loadTasks(ctx context.Context) ([]resources.ScheduledTasksRow, error) {

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
