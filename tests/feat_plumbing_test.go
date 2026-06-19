// Integration plumbing tests: idempotency ledger, web_routes URL table,
// and store-level primitives (web-route inventory, turn result/frames,
// run-log list) that underpin the cross-daemon contract.
package tests

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func TestFeature_IntegrationPlumbing(t *testing.T) {
	// First claim wins; replay of the same key returns the stored record.
	t.Run("idempotency-ledger", func(t *testing.T) {
		db, err := routd.OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		claimed, _, err := db.IdemClaim("/v1/messages", "k1", "hashA")
		if err != nil || !claimed {
			t.Fatalf("first claim = %v err=%v, want true", claimed, err)
		}
		claimed2, prior, err := db.IdemClaim("/v1/messages", "k1", "hashA")
		if err != nil {
			t.Fatal(err)
		}
		if claimed2 {
			t.Fatal("second claim of same key should lose")
		}
		if prior.RequestHash != "hashA" {
			t.Fatalf("prior hash = %q, want hashA", prior.RequestHash)
		}
	})

	// web_routes URL table: put → list → delete round-trip.
	t.Run("web-routes-table", func(t *testing.T) {
		db := mustRoutdDB(t)
		if err := db.PutWebRoute(routd.WebRouteRow{PathPrefix: "/pub/demo", Access: "public", Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		rows, err := db.WebRoutes("demo")
		if err != nil || len(rows) != 1 || rows[0].Access != "public" {
			t.Fatalf("web routes = %+v err=%v", rows, err)
		}
		ok, err := db.DeleteWebRoute("/pub/demo", "demo")
		if err != nil || !ok {
			t.Fatalf("delete web route = %v err=%v", ok, err)
		}
		rows, _ = db.WebRoutes("demo")
		if len(rows) != 0 {
			t.Fatalf("after delete = %d rows, want 0", len(rows))
		}
	})
}

// TestStore_WebRouteInventory covers store.AllWebRoutes (full listing) and
// store.MatchWebRoute (longest-prefix search) — both needed by proxyd's
// access-control decision path.
func TestStore_WebRouteInventory(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	for _, f := range []string{"a", "b"} {
		if err := s.PutGroup(core.Group{Folder: f, AddedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range []store.WebRoute{
		{PathPrefix: "/pub/a", Access: "public", Folder: "a", CreatedAt: now},
		{PathPrefix: "/pub/a/docs", Access: "auth", Folder: "a", CreatedAt: now},
		{PathPrefix: "/pub/b", Access: "deny", Folder: "b", CreatedAt: now},
	} {
		if err := s.SetWebRoute(r); err != nil {
			t.Fatal(err)
		}
	}

	all, err := s.AllWebRoutes()
	if err != nil || len(all) != 3 {
		t.Fatalf("AllWebRoutes = %d err=%v, want 3", len(all), err)
	}
	wr, ok := s.MatchWebRoute("/pub/a/docs/intro.html")
	if !ok || wr.PathPrefix != "/pub/a/docs" {
		t.Fatalf("MatchWebRoute longest-prefix = %q ok=%v, want /pub/a/docs", wr.PathPrefix, ok)
	}
	if _, ok := s.MatchWebRoute("/nope"); ok {
		t.Fatal("MatchWebRoute: unexpected match for /nope")
	}
}

// TestStore_TurnResult covers RecordTurnResult + GetTurnResult: the success
// path and the "pending" fallback for an unknown turn ID.
func TestStore_TurnResult(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, err := s.RecordTurnResult("hq", "turn-1", "sess-1", "success"); err != nil {
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
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	base := time.Now()
	for i := 0; i < 3; i++ {
		m := core.Message{
			ID: string(rune('a' + i)), ChatJID: "tg:1", Sender: "bot",
			Content: "frame", Timestamp: base.Add(time.Duration(i) * time.Second),
			BotMsg: true, TurnID: "turn-1",
		}
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := s.TurnFrames("turn-1", "", 200)
	if err != nil || len(frames) != 3 {
		t.Fatalf("TurnFrames = %d err=%v, want 3", len(frames), err)
	}
	page, err := s.TurnFrames("turn-1", frames[0].ID, 200)
	if err != nil || len(page) != 2 {
		t.Fatalf("TurnFrames after-cursor = %d err=%v, want 2", len(page), err)
	}
}

// TestStore_RunLogList covers AllRunLogs: task run records across tasks are
// returned ordered by time, providing the cross-task audit feed.
func TestStore_RunLogList(t *testing.T) {
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

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
