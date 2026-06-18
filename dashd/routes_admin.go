package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Routes")
	fmt.Fprint(w, `<p class="dim">Routing rules. Each inbound message walks the table in <code>seq</code> order; the first <code>match</code> hit pins the <code>target</code> group.</p>`)

	if d.adminDB() == nil {
		fmt.Fprint(w, htmlBanner("err", "routes store unavailable"))
		pageClose(w, r)
		return
	}

	rows, err := d.adminDB().Query(
		`SELECT id, seq, match, target,
		        COALESCE(observe_window_messages, 0),
		        COALESCE(observe_window_chars, 0)
		 FROM routes ORDER BY seq, id LIMIT 1000`)
	if err != nil {
		slog.Warn("routes: query", "err", err)
		fmt.Fprint(w, htmlBanner("err", "routes query error: "+err.Error()))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	var tableRows [][]string
	for rows.Next() {
		var id int64
		var seq, owm, owc int
		var match, target string
		if err := rows.Scan(&id, &seq, &match, &target, &owm, &owc); err != nil {
			slog.Warn("routes: scan", "err", err)
			continue
		}
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
	fmt.Fprint(w, htmlTable(
		[]string{"ID", "Seq", "Match", "Target", "Window (msgs/chars)", ""},
		tableRows,
		"No routes configured. Messages will not be delivered until at least one route exists.",
	))

	fmt.Fprint(w, htmlSection("Add route",
		`<form method="post" action="/dash/routes/">`+
			htmlFormRow("Seq", `<input type="number" name="seq" value="0" required>`)+
			htmlFormRow("Match", `<input type="text" name="match" placeholder="room=12345@g.us" required size="60">`)+
			htmlFormRow("Target", `<input type="text" name="target" placeholder="solo/inbox or solo/inbox#observe" required size="60">`)+
			htmlFormRow("Observe msgs", `<input type="number" name="observe_window_messages" value="0">`)+
			htmlFormRow("Observe chars", `<input type="number" name="observe_window_chars" value="0">`)+
			`<p><button type="submit">add</button></p>`+
			`</form>`+
			`<p class="dim">Match syntax: <code>room=&lt;jid-room&gt;</code> exact, or <code>verb=mention</code> filter.`+
			` Target <code>folder</code> trigger, <code>folder#observe</code> silent ingest, <code>folder#&lt;topic&gt;</code> topic pin.</p>`))

	pageClose(w, r)
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
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// PutRouteRow (not store.AddRoute): routd.db has no audit_log table, so the
	// audited writer would roll back — same audit-free discipline as the secrets
	// and grant rewires. Mirrors AddRoute's full column set so observe-window
	// columns are not dropped.
	id, err := store.New(d.adminDB()).PutRouteRow(core.Route{
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
		fmt.Fprintf(w, `{"id":%d}`, id)
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
	folder := core.ParseRouteTarget(body.Target).Folder
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	n, err := store.New(d.adminDB()).UpdateRouteRow(id, core.Route{
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
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "route.update",
		Actor:    "rest:dashd",
		Surface:  audit.SurfaceREST,
		Resource: fmt.Sprintf("routes/%d", id),
		Folder:   body.Target,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"seq":    body.Seq,
			"match":  body.Match,
			"target": body.Target,
		},
	})
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
	if d.adminDB() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Resolve folder via the existing route row so we can gate admin to scope.
	var target string
	if err := d.adminDB().QueryRow(`SELECT target FROM routes WHERE id = ?`, id).Scan(&target); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Warn("routes: lookup", "id", id, "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	folder := core.ParseRouteTarget(target).Folder
	if _, ok := d.requireAdmin(w, r, folder); !ok {
		return
	}
	if _, err := d.adminDB().Exec(`DELETE FROM routes WHERE id = ?`, id); err != nil {
		slog.Warn("routes: delete", "id", id, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	audit.Emit(context.Background(), audit.Event{
		Category: audit.CategoryMutation,
		Action:   "route.delete",
		Actor:    "rest:dashd",
		Surface:  audit.SurfaceREST,
		Resource: fmt.Sprintf("routes/%d", id),
		Folder:   folder,
		Outcome:  audit.OutcomeOK,
	})
	slog.Info("route deleted", "id", id)
	if r.Method == http.MethodPost {
		http.Redirect(w, r, "/dash/routes/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
