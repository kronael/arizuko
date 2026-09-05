package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/resreg/resources"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/router"
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
		rows, err := db.Query(`SELECT folder FROM groups ORDER BY folder`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var f string
			if err := rows.Scan(&f); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, map[string]string{"folder": f})
		}
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := db.Query(`SELECT id, owner, COALESCE(prompt,''), COALESCE(cron,''),
			status, created_at, COALESCE(next_run,'') FROM scheduled_tasks ORDER BY owner, id`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []resources.ScheduledTasksRow{}
		for rows.Next() {
			var t resources.ScheduledTasksRow
			if err := rows.Scan(&t.ID, &t.Owner, &t.Prompt, &t.Cron,
				&t.Status, &t.Created, &t.NextRun); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, t)
		}
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

	mux.HandleFunc("GET /v1/principals", func(w http.ResponseWriter, _ *http.Request) {
		rows, err := db.Query(`SELECT sub, COALESCE(name,'') FROM user_profiles
			UNION SELECT child, '' FROM acl_membership WHERE child NOT LIKE 'role:%' ORDER BY 1`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var sub, name string
			if err := rows.Scan(&sub, &name); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			out = append(out, map[string]string{"sub": sub, "name": name})
		}
		writeStubJSON(w, out)
	})

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

	// The held set (spec 6/35): chats with inbound rows that the route table
	// does not claim and that address no folder directly. The verdict is
	// router.ResolveRoute over the chat's last inbound row — the evaluator
	// routd's loop calls at its route-table step — so the stub cannot list a
	// chat the real reader would not.
	mux.HandleFunc("GET /v1/routing/unrouted", func(w http.ResponseWriter, _ *http.Request) {
		rrows, err := db.Query(`SELECT seq, match, target FROM routes ORDER BY seq, id`)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var routes []core.Route
		for rrows.Next() {
			var rt core.Route
			if err := rrows.Scan(&rt.Seq, &rt.Match, &rt.Target); err != nil {
				rrows.Close()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			routes = append(routes, rt)
		}
		rrows.Close()
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

	// One group. Its `open` and observe-window values are projections of the
	// `config` column with the SAME defaults the accessors use — absent open is
	// OPEN, an unset window is -1 — because a stub that defaulted them to false
	// and 0 would let a page pass while showing every group as closed and capped
	// at nothing.
	mux.HandleFunc("GET /v1/groups/{folder...}", func(w http.ResponseWriter, r *http.Request) {
		folder := r.PathValue("folder")
		var addedAt string
		var cfgJSON *string
		var open, owM, owC sql.NullInt64
		g := core.Group{Folder: folder}
		err := db.QueryRow(`SELECT added_at, container_config, product,
			COALESCE(json_extract(config,'$.model'),''),
			json_extract(config,'$.open'),
			json_extract(config,'$.observe_window_messages'),
			json_extract(config,'$.observe_window_chars')
			FROM groups WHERE folder = ?`, folder).
			Scan(&addedAt, &cfgJSON, &g.Product, &g.Model, &open, &owM, &owC)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		g.Open = !open.Valid || open.Int64 != 0
		g.ObserveWindowMessages, g.ObserveWindowChars = -1, -1
		if owM.Valid {
			g.ObserveWindowMessages = int(owM.Int64)
		}
		if owC.Valid {
			g.ObserveWindowChars = int(owC.Int64)
		}
		writeStubJSON(w, g)
	})

	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var t core.Task
		var cron, nextRun, ctxMode sql.NullString
		var created string
		err := db.QueryRow(`SELECT owner, chat_jid, COALESCE(prompt,''), cron,
			next_run, status, created_at, context_mode
			FROM scheduled_tasks WHERE id = ?`, id).
			Scan(&t.Owner, &t.ChatJID, &t.Prompt, &cron, &nextRun, &t.Status, &created, &ctxMode)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		t.ID, t.Cron, t.ContextMode = id, cron.String, ctxMode.String
		t.Created, _ = time.Parse(time.RFC3339, created)
		if nextRun.Valid {
			if nr, perr := time.Parse(time.RFC3339, nextRun.String); perr == nil {
				t.NextRun = &nr
			}
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

func writeStubJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
