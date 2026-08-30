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
	pageTopFor(w, r, "Routes")
	fmt.Fprint(w, `<p class="dim">Routing rules. Each inbound message walks the table in <code>seq</code> order; the first <code>match</code> hit pins the <code>target</code> group.</p>`)

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
		fmt.Fprint(w, htmlBanner("err", "could not reach routd for the route table"))
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
	sub, ok := d.requireAdmin(w, r, folder)
	if !ok {
		return
	}
	if d.adminDB() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// AddRoute, the audited writer: routd.db HAS had audit_log since routd
	// migration 0016, so the route and its audit row commit together — matching
	// the update and delete handlers below, which already audit. AddRoute's
	// plain INSERT matches PutRouteRow's INSERT OR IGNORE here because routes
	// has no unique constraint beyond the autoincrement id, so nothing to ignore.
	id, err := store.New(d.adminDB()).AsUser(sub).AddRoute(core.Route{
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
	if d.adminDB() == nil {
		http.Error(w, "routes store unavailable", http.StatusServiceUnavailable)
		return
	}
	// Authz on the CURRENT row's folder, not just the submitted target — an
	// admin on folder B must not be able to repoint a route that currently
	// targets folder A. Load the existing target first; 404 if it's gone.
	var curTarget string
	if err := d.adminDB().QueryRow(`SELECT target FROM routes WHERE id = ?`, id).Scan(&curTarget); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		slog.Warn("routes: update lookup", "id", id, "err", err)
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	curFolder := core.ParseRouteTarget(curTarget).Folder
	if _, ok := d.requireAdmin(w, r, curFolder); !ok {
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
