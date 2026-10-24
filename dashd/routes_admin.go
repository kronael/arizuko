package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/dashd/dashstore"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func decodeJSONBody(r *http.Request, out any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10)).Decode(out)
}

// Routes editor. GET renders the full table with an inline add form.
// POST adds, PATCH/DELETE by id. Auth: action="admin" scoped to the
// route's target folder (read from form/JSON). Reads view requires no
// admin gate — anyone signed in can inspect routes (parity with
// /dash/groups/).
func (d *dash) handleRoutes(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireUser(w, r); !ok {
		return
	}
	allowed, operator := d.callerScope(r)
	pageTopFor(w, r, "Routes")
	_, _ = fmt.Fprint(w, `<p class="dim">Routing rules. Each inbound message walks the table in <code>seq</code> order; the first <code>match</code> hit pins the <code>target</code> group.</p>`)

	// READ through routd's /v1 (spec 6/28 §"Read-path: /v1 only, no direct DB").
	// routd owns the routes table and already serves these rows to the agent, to
	// the CLI and to its own REST face; this page is one more reader, not a
	// second projection.
	//
	// This page has no per-folder authorize-before-read problem: it is
	// instance-wide and the operator gate ran above, before anything was read.
	// The WRITES below still go through the store — they carry the operator onto
	// the audit row via AsUser and dashd's service bearer does not, so moving
	// them waits on 6/29's identity decision.
	routes, err := d.listRoutes(r.Context())
	if err != nil {
		slog.Warn("routes: list", "err", err)
		_, _ = fmt.Fprint(w, htmlBanner("err", "could not reach routd for the route table"))
		pageClose(w, r)
		return
	}

	var tableRows [][]string
	for _, rt := range routes {
		id, seq, match, target := rt.ID, rt.Seq, rt.Match, rt.Target
		owm, owc := rt.ObserveWindowMessages, rt.ObserveWindowChars
		if !visible(allowed, operator, core.ParseRouteTarget(target).Folder) {
			continue
		}
		del := fmt.Sprintf(
			`<form method="post" action="/dash/routes/%d/delete" class="form-inline"`+
				` onsubmit="return confirm('delete route %d?')"><button type="submit">delete</button></form>`,
			id, id)
		tableRows = append(tableRows, []string{
			fmt.Sprintf(`<code>%d</code>`, id),
			fmt.Sprintf(`%d`, seq),
			fmt.Sprintf(`<code>%s</code>`, esc(match)),
			fmt.Sprintf(`<code>%s</code>`, esc(target)),
			fmt.Sprintf(`%d / %d`, owm, owc),
			del,
		})
	}
	_, _ = fmt.Fprint(w, htmlTable(
		[]string{"ID", "Seq", "Match", "Target", "Window (msgs/chars)", ""},
		tableRows,
		"No routes configured. Messages will not be delivered until at least one route exists.",
	))

	if operator {
		d.writeUnroutedSection(w, r)
	}

	_, _ = fmt.Fprint(w, htmlSection("Add route",
		`<form method="post" action="/dash/routes/">`+
			htmlFormRow("Seq", `<input type="number" name="seq" value="0" required>`)+
			htmlFormRow("Match", `<input type="text" name="match" placeholder="room=12345@g.us" required size="60">`)+
			htmlFormRow("Target", `<input type="text" name="target" placeholder="solo/inbox or solo/inbox#observe" required size="60">`)+
			htmlFormRow("Observe msgs", `<input type="number" name="observe_window_messages" value="0">`)+
			htmlFormRow("Observe chars", `<input type="number" name="observe_window_chars" value="0">`)+
			`<p><button type="submit">add</button></p>`+
			`</form>`+
			`<p class="dim">Match syntax: <code>room=&lt;jid-room&gt;</code> exact, or <code>verb=mention</code> filter.`+
			` Target <code>folder</code> trigger, <code>folder#observe</code> silent ingest, <code>folder#engaged</code> trigger only while the chat is engaged (else silent ingest), <code>folder#&lt;topic&gt;</code> topic pin.</p>`))

	pageClose(w, r)
}

// writeUnroutedSection renders the held set (spec 6/35-unrouted-traffic): every chat routd drops
// today because no route, direct address or engagement window claims it, each
// with a claim form that posts the existing add-route write. Operator-only — a
// held chat belongs to no folder, so no folder-scoped caller has a claim to
// see it. Read through routd's /v1, never the table: the verdict is the loop's
// own resolve, which only routd can run. An unreachable routd is an error
// banner, because "no unrouted chats" is a statement the operator acts on.
func (d *dash) writeUnroutedSection(w http.ResponseWriter, r *http.Request) {
	chats, err := d.listUnrouted(r.Context())
	if err != nil {
		slog.Warn("routes: unrouted", "err", err)
		_, _ = fmt.Fprint(w, htmlSection("Unrouted chats", htmlBanner("err", "could not reach routd for unrouted chats")))
		return
	}
	folders, err := d.foldersViaRoutd(r.Context())
	if err != nil {
		slog.Warn("routes: unrouted folders", "err", err)
		_, _ = fmt.Fprint(w, htmlSection("Unrouted chats", htmlBanner("err", "could not reach routd for the group list")))
		return
	}
	var rows [][]string
	for _, c := range chats {
		rows = append(rows, []string{
			esc(c.Platform),
			`<code>` + esc(c.ChatJID) + `</code>`,
			esc(c.ChatName),
			fmt.Sprint(c.Count),
			esc(shortDate(c.FirstAt)),
			esc(shortDate(c.LastAt)),
			`<code>` + esc(c.LastSender) + `</code>`,
			claimRouteForm(c.ChatJID, folders),
		})
	}
	_, _ = fmt.Fprint(w, htmlSection("Unrouted chats",
		`<p class="dim">Chats that have messaged this instance and reach no group: routd stores them and fires no turn.`+
			` <strong>Claim</strong> writes a <code>chat_jid=</code> route at seq 0; to claim a whole platform, add`+
			` <code>platform=&lt;name&gt;</code> below. History does not move — only messages after the claim reach the group.</p>`+
			htmlTable(
				[]string{"Platform", "Chat", "Name", "Messages", "First", "Last", "Last sender", ""},
				rows,
				"No unrouted chats: every chat that has messaged this instance resolves to a group.",
			)))
}

// claimRouteForm is the per-row claim: the add-route form with the match fixed
// to this chat and the target chosen from the groups routd lists.
func claimRouteForm(jid string, folders []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `<form method="post" action="/dash/routes/" class="form-inline">`+
		`<input type="hidden" name="seq" value="0">`+
		`<input type="hidden" name="match" value="chat_jid=%s">`+
		`<select name="target">`, esc(jid))
	for _, f := range folders {
		fmt.Fprintf(&b, `<option value="%s">%s</option>`, esc(f), esc(f))
	}
	b.WriteString(`</select> <button type="submit">claim</button></form>`)
	return b.String()
}

// shortDate trims an RFC3339 timestamp to the minute: "2026-09-05 11:14".
func shortDate(ts string) string {
	if t, ok := parseTS(ts); ok {
		return t.UTC().Format("2006-01-02 15:04")
	}
	return ts
}

// listUnrouted reads the held set from routd's /v1/routing/unrouted.
func (d *dash) listUnrouted(ctx context.Context) ([]apiv1.UnroutedChat, error) {
	body, status, err := d.routdGet(ctx, "/v1/routing/unrouted")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/routing/unrouted: status %d", status)
	}
	var out apiv1.UnroutedChatsResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}
	return out.Chats, nil
}

// routeFromForm parses seq/match/target from form-encoded body. JSON body
// is also accepted (Content-Type application/json) — keeps the API and
// the HTML form on one renderer.
type routeBody struct {
	Seq                   int    `json:"seq"`
	Match                 string `json:"match"`
	Target                string `json:"target"`
	ObserveWindowMessages int    `json:"observe_window_messages"`
	ObserveWindowChars    int    `json:"observe_window_chars"`
}

func parseRouteBody(r *http.Request) (routeBody, error) {
	var b routeBody
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		if err := decodeJSONBody(r, &b); err != nil {
			return b, err
		}
	} else {
		if err := r.ParseForm(); err != nil {
			return b, err
		}
		seq, err := strconv.Atoi(strings.TrimSpace(r.FormValue("seq")))
		if err != nil {
			return b, fmt.Errorf("seq: %w", err)
		}
		b.Seq = seq
		b.Match = strings.TrimSpace(r.FormValue("match"))
		b.Target = strings.TrimSpace(r.FormValue("target"))
		b.ObserveWindowMessages, _ = strconv.Atoi(strings.TrimSpace(r.FormValue("observe_window_messages")))
		b.ObserveWindowChars, _ = strconv.Atoi(strings.TrimSpace(r.FormValue("observe_window_chars")))
	}
	if b.Match == "" {
		return b, errors.New("match: empty")
	}
	if b.Target == "" {
		return b, errors.New("target: empty")
	}
	return b, nil
}

func (d *dash) handleRouteCreate(w http.ResponseWriter, r *http.Request) {
	body, err := parseRouteBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	folder := core.ParseRouteTarget(body.Target).Folder
	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.routdStore() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// AddRoute, the audited writer: routd.db HAS had audit_log since routd
	// migration 0016, so the route and its audit row commit together — matching
	// the update and delete handlers below, which already audit. AddRoute's
	// plain INSERT matches PutRouteRow's INSERT OR IGNORE here because routes
	// has no unique constraint beyond the autoincrement id, so nothing to ignore.
	id, err := d.routdStore().AsUser(sub).AddRoute(core.Route{
		Seq:                   body.Seq,
		Match:                 body.Match,
		Target:                body.Target,
		ObserveWindowMessages: body.ObserveWindowMessages,
		ObserveWindowChars:    body.ObserveWindowChars,
	})
	if err != nil {
		slog.Warn("routes: add", "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	slog.Info("route added", "id", id, "match", body.Match, "target", body.Target)
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{"id":%d}`, id)
		return
	}
	http.Redirect(w, r, "/dash/routes/", http.StatusSeeOther)
}

func (d *dash) handleRouteUpdate(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	body, err := parseRouteBody(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if d.routdStore() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Authz on the CURRENT row's folder, not just the submitted target — an
	// admin on folder B must not be able to repoint a route that currently
	// targets folder A. Load the existing target first; 404 if it's gone.
	curTarget, err := d.ds().RouteTarget(id)
	if err != nil {
		if errors.Is(err, dashstore.ErrRouteMissing) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Warn("routes: update lookup", "id", id, "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	curFolder := core.ParseRouteTarget(curTarget).Folder
	sub, ok := d.requireAdmin(w, r, curFolder)
	if !ok {
		return
	}
	// If the target moves to a different folder, the caller must also admin the
	// new folder — otherwise they could donate a route into a folder they own.
	newFolder := core.ParseRouteTarget(body.Target).Folder
	if newFolder != curFolder {
		if _, ok := d.requireAdmin(w, r, newFolder); !ok {
			return
		}
	}
	n, err := d.routdStore().AsUser(sub).UpdateRoute(id, core.Route{
		Seq:                   body.Seq,
		Match:                 body.Match,
		Target:                body.Target,
		ObserveWindowMessages: body.ObserveWindowMessages,
		ObserveWindowChars:    body.ObserveWindowChars,
	})
	if err != nil {
		slog.Warn("routes: update", "id", id, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("route updated", "id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (d *dash) handleRouteDelete(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	if d.routdStore() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Resolve folder via the existing route row so we can gate admin to scope.
	target, err := d.ds().RouteTarget(id)
	if err != nil {
		if errors.Is(err, dashstore.ErrRouteMissing) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Warn("routes: lookup", "id", id, "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	folder := core.ParseRouteTarget(target).Folder
	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	n, err := d.routdStore().AsUser(sub).DeleteRoute(id)
	if err != nil {
		slog.Warn("routes: delete", "id", id, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	if n == 0 {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	slog.Info("route deleted", "id", id)
	if r.Method == http.MethodPost {
		http.Redirect(w, r, "/dash/routes/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listRoutes reads the route table from routd's /v1 face.
//
// The rows decode into core.Route — the SAME struct routd's REST handler
// serves — so the page cannot render a field the API does not have. routd
// scopes that list to the caller's folder claim; dashd presents a service
// bearer, whose claim is empty, so it sees the whole table exactly as the
// direct read did. The per-folder narrowing this page applies is its own
// (`visible`, below), unchanged.
func (d *dash) listRoutes(ctx context.Context) ([]core.Route, error) {
	body, status, err := d.bearerCall(ctx, d.routdURL, http.MethodGet, "/v1/routes", nil)
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("routd /v1/routes: status %d", status)
	}
	var routes []core.Route
	if err := json.Unmarshal(body, &routes); err != nil {
		return nil, err
	}
	return routes, nil
}
