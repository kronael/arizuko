package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	pageTopFor(w, r, "Routes")
	fmt.Fprint(w, `<p class="dim">Routing rules. Each inbound message walks the table in <code>seq</code> order; the first <code>match</code> hit pins the <code>target</code> group.</p>`)

	if d.dbRW == nil {
		fmt.Fprint(w, `<div class="banner-err">routes store unavailable</div>`)
		pageClose(w, r)
		return
	}

	rows, err := d.dbRW.Query(
		`SELECT id, seq, match, target,
		        COALESCE(observe_window_messages, 0),
		        COALESCE(observe_window_chars, 0)
		 FROM routes ORDER BY seq, id LIMIT 1000`)
	if err != nil {
		slog.Warn("routes: query", "err", err)
		fmt.Fprintf(w, `<div class="banner-err">routes query error: %s</div>`, esc(err.Error()))
		pageClose(w, r)
		return
	}
	defer rows.Close()

	fmt.Fprint(w, `<table><thead><tr><th>ID</th><th>Seq</th><th>Match</th><th>Target</th><th>Window (msgs/chars)</th><th></th></tr></thead><tbody>`)
	var n int
	for rows.Next() {
		var id int64
		var seq, owm, owc int
		var match, target string
		if err := rows.Scan(&id, &seq, &match, &target, &owm, &owc); err != nil {
			slog.Warn("routes: scan", "err", err)
			continue
		}
		fmt.Fprintf(w, `<tr>
<td><code>%d</code></td>
<td>%d</td>
<td><code>%s</code></td>
<td><code>%s</code></td>
<td>%d / %d</td>
<td>
<form method="post" action="/dash/routes/%d/delete" style="display:inline" onsubmit="return confirm('delete route %d?')">
<button type="submit">delete</button>
</form>
</td>
</tr>`, id, seq, esc(match), esc(target), owm, owc, id, id)
		n++
	}
	if n == 0 {
		fmt.Fprint(w, `<tr><td colspan=6 class="empty">No routes. Add one below.</td></tr>`)
	}
	fmt.Fprint(w, `</tbody></table>`)

	fmt.Fprint(w, `<h2>Add route</h2>
<form method="post" action="/dash/routes/">
<p><label>Seq <input type="number" name="seq" value="0" required></label></p>
<p><label>Match <input type="text" name="match" placeholder="room=12345@g.us" required size="60"></label></p>
<p><label>Target <input type="text" name="target" placeholder="solo/inbox or solo/inbox#observe" required size="60"></label></p>
<p><button type="submit">add</button></p>
</form>
<p class="dim">Match syntax: <code>room=&lt;jid-room&gt;</code> exact, or <code>verb=mention</code> filter. Target <code>folder</code> trigger, <code>folder#observe</code> silent ingest, <code>folder#&lt;topic&gt;</code> topic pin.</p>`)

	// Delete uses POST /dash/routes/{id}/delete — register the alias here.
	pageClose(w, r)
}

// routeFromForm parses seq/match/target from form-encoded body. JSON body
// is also accepted (Content-Type application/json) — keeps the API and
// the HTML form on one renderer.
type routeBody struct {
	Seq    int    `json:"seq"`
	Match  string `json:"match"`
	Target string `json:"target"`
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
	if d.dbRW == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	s := store.New(d.dbRW)
	id, err := s.AddRoute(core.Route{Seq: body.Seq, Match: body.Match, Target: body.Target})
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
	if d.dbRW == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	res, err := d.dbRW.Exec(
		`UPDATE routes SET seq = ?, match = ?, target = ? WHERE id = ?`,
		body.Seq, body.Match, body.Target, id)
	if err != nil {
		slog.Warn("routes: update", "id", id, "err", err)
		http.Error(w, "write failed", http.StatusInternalServerError)
		return
	}
	n, _ := res.RowsAffected()
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
	if d.dbRW == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Resolve folder via the existing route row so we can gate admin to scope.
	var target string
	if err := d.dbRW.QueryRow(`SELECT target FROM routes WHERE id = ?`, id).Scan(&target); err != nil {
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
	if _, err := d.dbRW.Exec(`DELETE FROM routes WHERE id = ?`, id); err != nil {
		slog.Warn("routes: delete", "id", id, "err", err)
		http.Error(w, "delete failed", http.StatusInternalServerError)
		return
	}
	slog.Info("route deleted", "id", id)
	if r.Method == http.MethodPost {
		http.Redirect(w, r, "/dash/routes/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
