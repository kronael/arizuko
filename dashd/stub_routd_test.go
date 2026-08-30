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

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func writeStubJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
