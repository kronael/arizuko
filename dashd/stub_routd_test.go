package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/resreg/resources"
)

// stubRoutd stands in for routd's /v1 read face, serving from the SAME
// routd.db the test seeds.
//
// It exists because spec 6/28 moved dashd's reads off direct DB access: the
// pages ask routd now, so a test that seeds routd.db and renders a dashd page
// needs something to answer. tests/testutils ships no routd server, and
// standing the real one up here would drag its whole dependency graph into
// dashd's tests.
//
// It is deliberately a THIN projection of the same rows — it runs the query the
// owning daemon runs and marshals the owning daemon's struct. A stub that
// invented its own shape would let these tests pass against a page that cannot
// read the real thing.
func stubRoutd(t *testing.T, db *sql.DB) string {
	t.Helper()
	mux := http.NewServeMux()

	mux.HandleFunc("GET /v1/routes", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := db.Query(`SELECT id, seq, match, target,
			COALESCE(observe_window_messages,0), COALESCE(observe_window_chars,0)
			FROM routes ORDER BY seq, id`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []core.Route{}
		for rows.Next() {
			var r core.Route
			if err := rows.Scan(&r.ID, &r.Seq, &r.Match, &r.Target,
				&r.ObserveWindowMessages, &r.ObserveWindowChars); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, r)
		}
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/route_tokens", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT jid, owner_folder, COALESCE(target_folder,''),
			COALESCE(created_at,''), COALESCE(context,'')
			FROM route_tokens WHERE owner_folder = ? ORDER BY created_at DESC`,
			r.URL.Query().Get("folder"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []resources.RouteTokensRow{}
		for rows.Next() {
			var t resources.RouteTokensRow
			if err := rows.Scan(&t.JID, &t.OwnerFolder, &t.TargetFolder,
				&t.CreatedAt, &t.Context); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, t)
		}
		writeStubJSON(w, out)
	})

	// The acl list answers an ENVELOPE, {"folder":…,"acl":[…]}, and filters on an
	// exact scope match — both mirrored here, because a stub that returned a bare
	// array or a subtree would let the page pass against a shape routd does not
	// serve.
	mux.HandleFunc("GET /v1/acl", func(w http.ResponseWriter, r *http.Request) {
		folder := r.URL.Query().Get("folder")
		rows, err := db.Query(`SELECT principal, action, scope, effect,
			COALESCE(params,''), COALESCE(predicate,'')
			FROM acl WHERE scope = ? ORDER BY principal, action`, folder)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []core.ACLRow{}
		for rows.Next() {
			var a core.ACLRow
			if err := rows.Scan(&a.Principal, &a.Action, &a.Scope, &a.Effect,
				&a.Params, &a.Predicate); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, a)
		}
		writeStubJSON(w, map[string]any{"folder": folder, "acl": out})
	})

	// The spend axis. `since` is REQUIRED by routd — a summary with no window
	// reports a lifetime total as if it were current — so the stub refuses it
	// too, or a page that forgot to send one would pass here and fail live.
	mux.HandleFunc("GET /v1/cost/summary", func(w http.ResponseWriter, r *http.Request) {
		since := r.URL.Query().Get("since")
		if since == "" {
			http.Error(w, "since required", http.StatusBadRequest)
			return
		}
		rows, err := db.Query(`SELECT folder,
			COALESCE(SUM(input_tokens+output_tokens),0),
			COALESCE(SUM(cost_cents),0)
			FROM cost_log WHERE recorded_at >= ? GROUP BY folder ORDER BY folder`, since)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type row struct {
			Folder    string `json:"folder"`
			Tokens    int    `json:"tokens"`
			CostCents int    `json:"cost_cents"`
		}
		out := []row{}
		for rows.Next() {
			var x row
			if err := rows.Scan(&x.Folder, &x.Tokens, &x.CostCents); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, x)
		}
		writeStubJSON(w, out)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func writeStubJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
