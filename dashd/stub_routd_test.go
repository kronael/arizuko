package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/objstore/sqlstore"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
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

	// routd serves the group list as [{"folder":…}] — the shape the live
	// /v1/groups answers, which is what dashd's pages now read instead of the
	// table (BUGS L142).
	mux.HandleFunc("GET /v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		// The folder IS the coordinate, so the read arrives in folder order.
		folders, err := store.New(db).GroupFolders(0)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := []map[string]string{}
		for _, f := range folders {
			out = append(out, map[string]string{"folder": f})
		}
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		items, err := stubItems(r, db, store.ScheduledTasks)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := []resources.ScheduledTasksRow{}
		for _, it := range items {
			out = append(out, stubTaskRow(it))
		}
		slices.SortFunc(out, func(a, b resources.ScheduledTasksRow) int {
			if a.Owner != b.Owner {
				return strings.Compare(a.Owner, b.Owner)
			}
			return strings.Compare(a.ID, b.ID)
		})
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/sessions/counts", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := db.Query(`SELECT group_folder, COUNT(*) FROM sessions
			GROUP BY group_folder ORDER BY group_folder`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var f string
			var n int
			if err := rows.Scan(&f, &n); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, map[string]any{"folder": f, "count": n})
		}
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/messages/volume", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := db.Query(`SELECT DATE(timestamp) AS day, COUNT(*) FROM messages
			WHERE is_bot_message=0 AND timestamp >= datetime('now','-7 days')
			GROUP BY day ORDER BY day DESC`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var d string
			var n int
			if err := rows.Scan(&d, &n); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, map[string]any{"day": d, "count": n})
		}
		writeStubJSON(w, out)
	})

	// Honours before+limit like routd's handler: the pagination test walks two
	// pages, and a stub that ignores `before` returns page one twice.
	mux.HandleFunc("GET /v1/messages/activity", func(w http.ResponseWriter, r *http.Request) {
		q := `SELECT timestamp, source, chat_jid, sender, COALESCE(verb,''),
			substr(content,1,80) FROM messages`
		args := []any{}
		if before := r.URL.Query().Get("before"); before != "" {
			q += ` WHERE timestamp < ?`
			args = append(args, before)
		}
		limit := 1000
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v < limit {
			limit = v
		}
		q += ` ORDER BY timestamp DESC LIMIT ?`
		args = append(args, limit)
		rows, err := db.Query(q, args...)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var ts, src, jid, sender, verb, prev string
			if err := rows.Scan(&ts, &src, &jid, &sender, &verb, &prev); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, map[string]string{"timestamp": ts, "source": src,
				"chat_jid": jid, "sender": sender, "verb": verb, "preview": prev})
		}
		writeStubJSON(w, out)
	})

	// TWO reads and a merge, exactly as routd's handlePrincipals does it: two
	// entities and no join to run. A `role:` child is a role, not a person.
	mux.HandleFunc("GET /v1/principals", func(w http.ResponseWriter, r *http.Request) {
		names, err := store.New(db).ProfileNames(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		items, err := stubItems(r, db, store.ACLMembership)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, it := range items {
			child, _, ok := strings.Cut(it.Key.Sort, store.KeySep)
			if !ok || strings.HasPrefix(child, "role:") {
				continue
			}
			if _, seen := names[child]; !seen {
				names[child] = ""
			}
		}
		out := []map[string]string{}
		for sub, name := range names {
			out = append(out, map[string]string{"sub": sub, "name": name})
		}
		slices.SortFunc(out, func(a, b map[string]string) int {
			return strings.Compare(a["sub"], b["sub"])
		})
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/routes", func(w http.ResponseWriter, _ *http.Request) {
		// RouteTable is `ORDER BY seq, id` — precedence, then write order.
		out, err := store.New(db).RouteTable()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []core.Route{}
		}
		writeStubJSON(w, out)
	})

	// The held set (spec 6/35): chats with inbound rows that the route table
	// does not claim and that address no folder directly. The verdict is
	// router.ResolveRoute over the chat's last inbound row — the evaluator
	// routd's loop calls at its route-table step — so the stub cannot list a
	// chat the real reader would not.
	mux.HandleFunc("GET /v1/routing/unrouted", func(w http.ResponseWriter, _ *http.Request) {
		routes, err := store.New(db).RouteTable()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// One MAX() so the bare sender/verb columns come from the newest row
		// (SQLite's single-max rule); MIN() rides a subquery for that reason.
		rows, err := db.Query(`SELECT chat_jid, sender, verb, COUNT(*),
			(SELECT MIN(m2.timestamp) FROM messages m2 WHERE m2.chat_jid=messages.chat_jid
			   AND m2.is_from_me=0 AND m2.is_bot_message=0),
			MAX(timestamp)
			FROM messages WHERE is_from_me=0 AND is_bot_message=0
			GROUP BY chat_jid ORDER BY MAX(timestamp) DESC`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := apiv1.UnroutedChatsResponse{Chats: []apiv1.UnroutedChat{}}
		for rows.Next() {
			var c apiv1.UnroutedChat
			var verb string
			if err := rows.Scan(&c.ChatJID, &c.LastSender, &verb, &c.Count, &c.FirstAt, &c.LastAt); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if groupfolder.JidFolder(c.ChatJID) != "" || !strings.Contains(c.ChatJID, ":") {
				continue
			}
			if router.ResolveRoute(core.Message{ChatJID: c.ChatJID, Sender: c.LastSender, Verb: verb}, routes) != "" {
				continue
			}
			c.Platform = core.JidPlatform(c.ChatJID)
			out.Chats = append(out.Chats, c)
		}
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/route_tokens", func(w http.ResponseWriter, r *http.Request) {
		folder := r.URL.Query().Get("folder")
		items, err := stubItems(r, db, store.RouteTokens)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := []resources.RouteTokensRow{}
		for _, it := range items {
			if it.Attrs["owner_folder"] != folder {
				continue
			}
			out = append(out, resources.RouteTokensRow{
				TokenHash:    it.Key.Sort,
				JID:          it.Attrs["jid"],
				OwnerFolder:  it.Attrs["owner_folder"],
				TargetFolder: it.Attrs["target_folder"],
				CreatedAt:    it.Attrs["created_at"],
				Context:      it.Attrs["context"],
			})
		}
		slices.SortFunc(out, func(a, b resources.RouteTokensRow) int {
			return strings.Compare(b.CreatedAt, a.CreatedAt)
		})
		writeStubJSON(w, out)
	})

	// The acl list answers an ENVELOPE, {"folder":…,"acl":[…]}, and filters on an
	// exact scope match — both mirrored here, because a stub that returned a bare
	// array or a subtree would let the page pass against a shape routd does not
	// serve.
	mux.HandleFunc("GET /v1/acl", func(w http.ResponseWriter, r *http.Request) {
		folder := r.URL.Query().Get("folder")
		out, err := store.New(db).ListACLByScope(folder)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		slices.SortFunc(out, func(a, b core.ACLRow) int {
			if a.Principal != b.Principal {
				return strings.Compare(a.Principal, b.Principal)
			}
			return strings.Compare(a.Action, b.Action)
		})
		writeStubJSON(w, map[string]any{"folder": folder, "acl": out})
	})

	// The spend axis. routd answers it from the model gateway, which is the
	// ledger — the numbers are in nobody's SQLite, so the stub serves what a test
	// seeds through stubMeter rather than reading a table (spec 6/39).
	//
	// `since` is REQUIRED by routd — a summary with no window reports a lifetime
	// total as if it were current — so the stub refuses it too, or a page that
	// forgot to send one would pass here and fail live.
	mux.HandleFunc("GET /v1/cost/summary", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("since") == "" {
			http.Error(w, "since required", http.StatusBadRequest)
			return
		}
		rows := stubSpend
		if rows == nil {
			rows = []stubSpendRow{}
		}
		writeStubJSON(w, map[string]any{"metered": stubMetered, "folders": rows})
	})

	// One group. Its `open` and observe-window values are projections of the
	// `config` column with the SAME defaults the accessors use — absent open is
	// OPEN, an unset window is -1 — because a stub that defaulted them to false
	// and 0 would let a page pass while showing every group as closed and capped
	// at nothing.
	mux.HandleFunc("GET /v1/groups/{folder...}", func(w http.ResponseWriter, r *http.Request) {
		g, ok := store.New(db).GroupByFolder(r.PathValue("folder"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeStubJSON(w, g)
	})

	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, ok, err := store.New(db).TaskByID(r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeStubJSON(w, t)
	})

	mux.HandleFunc("GET /v1/tasks/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(`SELECT run_at, status, COALESCE(duration_ms,0), COALESCE(error,'')
			FROM task_run_logs WHERE task_id = ? ORDER BY run_at DESC LIMIT 20`, r.PathValue("id"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		type run struct {
			RunAt      string `json:"run_at"`
			Status     string `json:"status"`
			DurationMS int    `json:"duration_ms"`
			Error      string `json:"error"`
		}
		out := []run{}
		for rows.Next() {
			var x run
			if err := rows.Scan(&x.RunAt, &x.Status, &x.DurationMS, &x.Error); err != nil {
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

// stubItems reads one whole entity partition, for the two endpoints whose
// projection store has no reader for: the tasks list keys on (owner, id) and
// the token list has to carry the digest the resource declares.
func stubItems(r *http.Request, db *sql.DB, entity string) ([]objstore.Item, error) {
	return sqlstore.New(db).Query(r.Context(), objstore.Query{
		Entity: entity, Partition: store.OwnerRoutd,
	})
}

// stubTaskRow renders one task item the way resreg's own entity read does: the
// attributes as they are stored, with the coordinate as the id.
func stubTaskRow(it objstore.Item) resources.ScheduledTasksRow {
	return resources.ScheduledTasksRow{
		ID:          it.Key.Sort,
		Owner:       it.Attrs["owner"],
		ChatJID:     it.Attrs["chat_jid"],
		Prompt:      it.Attrs["prompt"],
		Cron:        it.Attrs["cron"],
		NextRun:     it.Attrs["next_run"],
		Status:      it.Attrs["status"],
		Created:     it.Attrs["created_at"],
		ContextMode: it.Attrs["context_mode"],
	}
}

func writeStubJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// stubSpendRow is one folder's metered spend in the stub's /v1/cost/summary
// reply, in the shape routd emits.
type stubSpendRow struct {
	Folder    string `json:"folder"`
	Tokens    int    `json:"tokens"`
	CostCents int    `json:"cost_cents"`
}

// stubMetered / stubSpend are the meter the stub reports. They default to a
// metered instance with no spend, which is what a fresh gateway looks like.
// dashd's tests do not run in parallel, so a package-level pair is the whole
// mechanism; stubMeter resets both with the test.
var (
	stubMetered = true
	stubSpend   []stubSpendRow
)

// stubMeter seeds what the gateway metered for the duration of one test.
func stubMeter(t *testing.T, metered bool, rows ...stubSpendRow) {
	t.Helper()
	stubMetered, stubSpend = metered, rows
	t.Cleanup(func() { stubMetered, stubSpend = true, nil })
}
