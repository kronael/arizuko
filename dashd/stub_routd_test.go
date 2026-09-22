package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
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
func stubRoutd(t *testing.T, db objstore.Store) string {
	t.Helper()
	mux := http.NewServeMux()

	// routd serves the group list as [{"folder":…}] — the shape the live
	// /v1/groups answers, which is what dashd's pages now read instead of the
	// table (BUGS L142).
	mux.HandleFunc("GET /v1/groups", func(w http.ResponseWriter, _ *http.Request) {
		// The folder IS the coordinate, so the read arrives in folder order.
		folders, err := store.NewOver(db).GroupFolders(0)
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

	// Both tallies come from the SAME readers routd's own handlers answer with
	// (routd/dash_reads_http.go), so a stub cannot agree with the page about a
	// shape the daemon does not serve.
	mux.HandleFunc("GET /v1/sessions/counts", func(w http.ResponseWriter, _ *http.Request) {
		perFolder, err := store.NewOver(db).TopicsPerFolder()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		folders := make([]string, 0, len(perFolder))
		for f := range perFolder {
			folders = append(folders, f)
		}
		sort.Strings(folders)
		out := []map[string]any{}
		for _, f := range folders {
			out = append(out, map[string]any{"folder": f, "count": perFolder[f]})
		}
		writeStubJSON(w, out)
	})

	mux.HandleFunc("GET /v1/messages/volume", func(w http.ResponseWriter, r *http.Request) {
		counts, err := store.NewOver(db).DailyVolume(r.Context(), 7)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		days := make([]string, 0, len(counts))
		for d := range counts {
			days = append(days, d)
		}
		sort.Sort(sort.Reverse(sort.StringSlice(days)))
		out := []map[string]any{}
		for _, d := range days {
			out = append(out, map[string]any{"day": d, "count": counts[d]})
		}
		writeStubJSON(w, out)
	})

	// Honours before+limit like routd's handler: the pagination test walks two
	// pages, and a stub that ignores `before` returns page one twice. The feed
	// is the activity ENTITY — one partition, newest first, carrying the
	// preview its writer truncated — so the stub reads it the way routd does.
	mux.HandleFunc("GET /v1/messages/activity", func(w http.ResponseWriter, r *http.Request) {
		limit := 1000
		if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 && v < limit {
			limit = v
		}
		rows, err := store.NewOver(db).RecentActivity(r.Context(),
			r.URL.Query().Get("before"), limit)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := []map[string]string{}
		for _, a := range rows {
			out = append(out, map[string]string{"timestamp": a.Timestamp, "source": a.Source,
				"chat_jid": a.ChatJID, "sender": a.Sender, "verb": a.Verb, "preview": a.Preview})
		}
		writeStubJSON(w, out)
	})

	// TWO reads and a merge, exactly as routd's handlePrincipals does it: two
	// entities and no join to run. A `role:` child is a role, not a person.
	mux.HandleFunc("GET /v1/principals", func(w http.ResponseWriter, r *http.Request) {
		names, err := store.NewOver(db).ProfileNames(r.Context())
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
		out, err := store.NewOver(db).RouteTable()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if out == nil {
			out = []core.Route{}
		}
		writeStubJSON(w, out)
	})

	// The held set (spec 6/35-unrouted-traffic): chats with inbound rows that the route table
	// does not claim and that address no folder directly. The verdict is
	// router.ResolveRoute over the chat's last inbound row — the evaluator
	// routd's loop calls at its route-table step — so the stub cannot list a
	// chat the real reader would not.
	mux.HandleFunc("GET /v1/routing/unrouted", func(w http.ResponseWriter, r *http.Request) {
		routes, err := store.NewOver(db).RouteTable()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// The candidate list is the inbound summary routd's own reader walks,
		// and the verdict per chat is resolve over that chat's newest inbound
		// row — the same pair, so the stub cannot list a chat the real reader
		// would not.
		st := store.NewOver(db)
		summary, err := st.InboundChatSummary(r.Context())
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := apiv1.UnroutedChatsResponse{Chats: []apiv1.UnroutedChat{}}
		for _, s := range summary {
			last, ok, err := st.LastInboundMessage(r.Context(), s.ChatJID)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if !ok {
				continue
			}
			if groupfolder.JidFolder(s.ChatJID) != "" || !strings.Contains(s.ChatJID, ":") {
				continue
			}
			if router.ResolveRoute(last, routes) != "" {
				continue
			}
			out.Chats = append(out.Chats, apiv1.UnroutedChat{
				ChatJID: s.ChatJID, Platform: core.JidPlatform(s.ChatJID),
				ChatName: last.ChatName, Count: s.Count, LastSender: last.Sender,
				FirstAt: s.FirstAt.UTC().Format(time.RFC3339Nano),
				LastAt:  s.LastAt.UTC().Format(time.RFC3339Nano),
			})
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
		out, err := store.NewOver(db).ListACLByScope(folder)
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
		g, ok := store.NewOver(db).GroupByFolder(r.PathValue("folder"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeStubJSON(w, g)
	})

	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		t, ok, err := store.NewOver(db).TaskByID(r.PathValue("id"))
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

	// The per-task run history, read the way routd's own handler reads it: the
	// entity, newest first, through the one method that renders a run log.
	mux.HandleFunc("GET /v1/tasks/{id}/runs", func(w http.ResponseWriter, r *http.Request) {
		type run struct {
			RunAt      string `json:"run_at"`
			Status     string `json:"status"`
			DurationMS int64  `json:"duration_ms"`
			Error      string `json:"error"`
		}
		out := []run{}
		for _, l := range store.NewOver(db).TaskRunLogs(r.PathValue("id"), 20) {
			out = append(out, run{
				RunAt:      l.RunAt.Format(time.RFC3339),
				Status:     l.Status,
				DurationMS: l.DurationMS,
				Error:      l.Error,
			})
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
func stubItems(r *http.Request, db objstore.Store, entity string) ([]objstore.Item, error) {
	return db.Query(r.Context(), objstore.Query{
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

// stubOnbod serves the one onbod read dashd makes, GET /v1/invites. onbod's
// admin is package main, so its real resource cannot be mounted here; this
// mirrors the shape onbod answers (onbod/admin.go inviteJSON), and a drift
// between the two is what TestInviteRowMatchesOnbodsWireShape pins.
func stubOnbod(t *testing.T, db objstore.Store) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/invites", func(w http.ResponseWriter, _ *http.Request) {
		invs, err := store.NewOver(db).ListInvites("")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := make([]map[string]any, len(invs))
		for i, inv := range invs {
			row := map[string]any{
				"ref":           inv.Ref,
				"target_glob":   inv.TargetGlob,
				"issued_by_sub": inv.IssuedBySub,
				"issued_at":     inv.IssuedAt.Format(time.RFC3339),
				"max_uses":      inv.MaxUses,
				"used_count":    inv.UsedCount,
			}
			if inv.ExpiresAt != nil {
				row["expires_at"] = inv.ExpiresAt.Format(time.RFC3339)
			}
			out[i] = row
		}
		writeStubJSON(w, map[string]any{"invites": out})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}
