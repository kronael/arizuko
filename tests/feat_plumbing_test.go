// Integration plumbing tests: web_routes URL table and store-level primitives
// (web-route inventory, turn result/frames, run-log list) that underpin the
// cross-daemon contract.
package tests

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestFeature_IntegrationPlumbing(t *testing.T) {
	// routd's web_routes reader over rows that exist. The items are addressed
	// at the coordinate store declares, because routd's own writers are
	// tx-scoped behind resreg; the subject here is the reader.
	t.Run("web-routes-table", func(t *testing.T) {
		db := mustRoutdDB(t)
		putWebRouteItem(t, db.Obj(), "/pub/demo", "public", "demo")
		rows, err := db.WebRoutes("demo")
		if err != nil || len(rows) != 1 || rows[0].Access != "public" {
			t.Fatalf("web routes = %+v err=%v", rows, err)
		}
		deleteWebRouteItem(t, db.Obj(), "/pub/demo")
		rows, _ = db.WebRoutes("demo")
		if len(rows) != 0 {
			t.Fatalf("after delete = %d rows, want 0", len(rows))
		}
	})
}

// TestStore_WebRouteInventory covers store.AllWebRoutes — the whole-table
// snapshot proxyd reads before matching a request. proxyd does the
// longest-prefix match itself, in Go, over that snapshot (proxyd/main.go:159),
// so the ordering this asserts is the contract and there is no SQL matcher.
func TestStore_WebRouteInventory(t *testing.T) {
	s := testutils.NewRoutdStore(t)

	now := time.Now()
	for _, f := range []string{"a", "b"} {
		if err := s.PutGroup(core.Group{Folder: f, AddedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range [][2]string{
		{"/pub/a", "a"}, {"/pub/a/docs", "a"}, {"/pub/b", "b"},
	} {
		putWebRouteItem(t, s.Obj(), r[0], "public", r[1])
	}

	all, err := s.AllWebRoutes()
	if err != nil || len(all) != 3 {
		t.Fatalf("AllWebRoutes = %d err=%v, want 3", len(all), err)
	}
	// Ordered by path_prefix, so proxyd's snapshot is stable across reads.
	for i, want := range []string{"/pub/a", "/pub/a/docs", "/pub/b"} {
		if all[i].PathPrefix != want {
			t.Errorf("AllWebRoutes[%d] = %q, want %q", i, all[i].PathPrefix, want)
		}
	}
}

// TestStore_TurnResult covers RecordTurnResult + GetTurnResult: the success
// path and the "pending" fallback for an unknown turn ID.
func TestStore_TurnResult(t *testing.T) {
	s := testutils.NewRoutdStore(t)

	if _, err := s.RecordTurnResult("hq", "turn-1", "sess-1", "success", ""); err != nil {
		t.Fatal(err)
	}
	ti, err := s.GetTurnResult("hq", "turn-1")
	if err != nil {
		t.Fatal(err)
	}
	if ti.Status != "success" || ti.SessionID != "sess-1" {
		t.Fatalf("GetTurnResult = %+v", ti)
	}
	pending, err := s.GetTurnResult("hq", "missing")
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != "pending" {
		t.Fatalf("unknown turn: want pending, got %q", pending.Status)
	}
}

// TestStore_TurnFrames covers TurnFrames pagination: full fetch then
// after-cursor fetch returns the tail only.
func TestStore_TurnFrames(t *testing.T) {
	s := testutils.NewRoutdStore(t)

	base := time.Now()
	for i := range 3 {
		m := core.Message{
			ID: string(rune('a' + i)), ChatJID: "tg:1", Sender: "bot",
			Content: "frame", Timestamp: base.Add(time.Duration(i) * time.Second),
			BotMsg: true, TurnID: "turn-1",
		}
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := s.TurnFrames("tg:1", "turn-1", "", 200)
	if err != nil || len(frames) != 3 {
		t.Fatalf("TurnFrames = %d err=%v, want 3", len(frames), err)
	}
	page, err := s.TurnFrames("tg:1", "turn-1", frames[0].ID, 200)
	if err != nil || len(page) != 2 {
		t.Fatalf("TurnFrames after-cursor = %d err=%v, want 2", len(page), err)
	}
}

// TestStore_RunLogList covers AllRunLogs: task run records across tasks are
// returned ordered by time, providing the cross-task audit feed.
func TestStore_RunLogList(t *testing.T) {
	s := testutils.NewRoutdStore(t)

	for _, tid := range []string{"t1", "t2"} {
		if err := s.CreateTask(core.Task{
			ID: tid, Owner: "hq", ChatJID: "tg:1", Prompt: "p",
			Status: core.TaskActive, Created: time.Now(),
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.RecordTaskRun(store.TaskRunLog{TaskID: tid, Status: "ok", DurationMS: 5}); err != nil {
			t.Fatal(err)
		}
	}
	logs := s.AllRunLogs(10)
	if len(logs) != 2 {
		t.Fatalf("AllRunLogs = %d, want 2", len(logs))
	}
}

// putWebRouteItem lands one web access rule at the coordinate store declares
// for it, with the attribute names store exports. routd owns every write of
// these items and does it on resreg's transaction (store/web_routes.go), so a
// fixture that needs one outside a request addresses the item itself rather
// than growing a second writer.
func putWebRouteItem(t *testing.T, d objstore.Store, pathPrefix, access, folder string) {
	t.Helper()
	key, err := store.WebRouteKey(pathPrefix)
	if err != nil {
		t.Fatalf("coordinate of %s: %v", pathPrefix, err)
	}
	if _, err := d.GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Put, Entity: store.WebRoutes, Key: key,
		Attrs: map[string]string{
			store.AttrWebAccess:    access,
			store.AttrWebFolder:    folder,
			store.AttrWebCreatedAt: core.NowTS(),
		},
	}}); err != nil {
		t.Fatalf("seed web route %s: %v", pathPrefix, err)
	}
}

// deleteWebRouteItem removes one rule by its coordinate.
func deleteWebRouteItem(t *testing.T, d objstore.Store, pathPrefix string) {
	t.Helper()
	key, err := store.WebRouteKey(pathPrefix)
	if err != nil {
		t.Fatalf("coordinate of %s: %v", pathPrefix, err)
	}
	if _, err := d.GuardedBatch(t.Context(), nil, []objstore.Write{{
		Op: objstore.Delete, Entity: store.WebRoutes, Key: key,
	}}); err != nil {
		t.Fatalf("remove web route %s: %v", pathPrefix, err)
	}
}
