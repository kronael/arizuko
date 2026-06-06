package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// stubRouter records the calls timed's split fire loop makes, standing in for
// routd's HTTP surface. due() returns the seeded tasks once, then nothing (so a
// re-tick doesn't re-fire); enqueue/runlog/reschedule capture their bodies.
type stubRouter struct {
	mu          sync.Mutex
	due         []dueTask
	served      bool
	enqueued    []apiv1.Message
	runlogs     []map[string]any
	reschedules []rescheduleCall
}

type rescheduleCall struct {
	ID      string
	NextRun string `json:"next_run"`
	Status  string `json:"status"`
}

func (s *stubRouter) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tasks/due", func(w http.ResponseWriter, _ *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := s.due
		if s.served {
			out = nil
		}
		s.served = true
		json.NewEncoder(w).Encode(map[string]any{"tasks": out})
	})
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		var m apiv1.Message
		json.NewDecoder(r.Body).Decode(&m)
		s.mu.Lock()
		s.enqueued = append(s.enqueued, m)
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/tasks/runlog", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		s.mu.Lock()
		s.runlogs = append(s.runlogs, b)
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/tasks/{id}/reschedule", func(w http.ResponseWriter, r *http.Request) {
		var c rescheduleCall
		json.NewDecoder(r.Body).Decode(&c)
		c.ID = r.PathValue("id")
		s.mu.Lock()
		s.reschedules = append(s.reschedules, c)
		s.mu.Unlock()
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	return mux
}

// TestFireSplitDrivesRoutd: one federated tick claims a due cron task and drives
// enqueue → runlog → reschedule against the routd stub, with no local DB.
func TestFireSplitDrivesRoutd(t *testing.T) {
	stub := &stubRouter{due: []dueTask{
		{ID: "t1", ChatJID: "web:main", Prompt: "hello", Cron: "0 9 * * *", ContextMode: "group"},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	r := &router{base: srv.URL, http: srv.Client(), tz: "UTC"}
	r.fireSplit(context.Background())

	if len(stub.enqueued) != 1 {
		t.Fatalf("enqueued = %d want 1", len(stub.enqueued))
	}
	m := stub.enqueued[0]
	if m.ChatJID != "web:main" || m.Content != "hello" || m.Sender != "timed" {
		t.Errorf("enqueued message wrong: %+v", m)
	}
	if len(stub.runlogs) != 1 || stub.runlogs[0]["status"] != "success" {
		t.Errorf("runlogs = %+v want one success", stub.runlogs)
	}
	if len(stub.reschedules) != 1 {
		t.Fatalf("reschedules = %d want 1", len(stub.reschedules))
	}
	rc := stub.reschedules[0]
	if rc.ID != "t1" || rc.Status != "active" {
		t.Errorf("reschedule = %+v want t1/active", rc)
	}
	if nr, err := time.Parse(time.RFC3339, rc.NextRun); err != nil || !nr.After(time.Now()) {
		t.Errorf("reschedule next_run = %q want future RFC3339", rc.NextRun)
	}
}

// TestFireSplitOneShotCompletes: a one-shot task (no cron) reschedules to
// completed with an empty next_run.
func TestFireSplitOneShotCompletes(t *testing.T) {
	stub := &stubRouter{due: []dueTask{
		{ID: "once", ChatJID: "web:main", Prompt: "do it", Cron: "", ContextMode: "group"},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	r := &router{base: srv.URL, http: srv.Client(), tz: "UTC"}
	r.fireSplit(context.Background())

	if len(stub.reschedules) != 1 {
		t.Fatalf("reschedules = %d want 1", len(stub.reschedules))
	}
	rc := stub.reschedules[0]
	if rc.Status != "completed" || rc.NextRun != "" {
		t.Errorf("one-shot reschedule = %+v want completed/empty next_run", rc)
	}
}

// TestFireSplitIsolatedSender: an isolated-context task enqueues with a
// timed-isolated:<id> sender, mirroring the monolith.
func TestFireSplitIsolatedSender(t *testing.T) {
	stub := &stubRouter{due: []dueTask{
		{ID: "iso", ChatJID: "web:main", Prompt: "p", ContextMode: "isolated"},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	r := &router{base: srv.URL, http: srv.Client(), tz: "UTC"}
	r.fireSplit(context.Background())

	if len(stub.enqueued) != 1 || stub.enqueued[0].Sender != "timed-isolated:iso" {
		t.Errorf("isolated sender wrong: %+v", stub.enqueued)
	}
}

// TestFireSplitOpensNoDB: the split fire loop touches no messages.db. Run a full
// tick inside an isolated dir wired as DATA_DIR/cwd and assert no SQLite file
// (messages.db / store/) was created — timed in split mode is DB-free.
func TestFireSplitOpensNoDB(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DATA_DIR", dir)
	t.Setenv("DATABASE", filepath.Join(dir, "store", "messages.db"))

	stub := &stubRouter{due: []dueTask{
		{ID: "t1", ChatJID: "web:main", Prompt: "hi", Cron: "0 9 * * *"},
	}}
	srv := httptest.NewServer(stub.handler())
	defer srv.Close()

	r := &router{base: srv.URL, http: srv.Client(), tz: "UTC"}
	r.fireSplit(context.Background())

	// The tick drove routd (proof it ran), and nothing on disk was opened.
	if len(stub.enqueued) != 1 {
		t.Fatalf("split tick did not drive routd: enqueued=%d", len(stub.enqueued))
	}
	walkNoDB(t, dir)
	walkNoDB(t, ".") // cwd: a stray sql.Open("messages.db?...") would land here
}

// walkNoDB fails if any SQLite artifact exists under root.
func walkNoDB(t *testing.T, root string) {
	t.Helper()
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		switch ext := filepath.Ext(p); ext {
		case ".db", ".db-wal", ".db-shm":
			t.Errorf("split mode created a DB file: %s", p)
		}
		return nil
	})
}

// TestFireSplitEnqueueErrorRestoresActive: a routd enqueue failure logs an error
// run and reschedules the task back to active (so the next tick re-fires).
func TestFireSplitEnqueueErrorRestoresActive(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/tasks/due", func(w http.ResponseWriter, _ *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"tasks": []dueTask{
			{ID: "e1", ChatJID: "web:main", Prompt: "boom", Cron: "0 9 * * *"},
		}})
	})
	var reschedules []rescheduleCall
	var runlogs []map[string]any
	mux.HandleFunc("POST /v1/messages", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	mux.HandleFunc("POST /v1/tasks/runlog", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		json.NewDecoder(r.Body).Decode(&b)
		runlogs = append(runlogs, b)
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/tasks/{id}/reschedule", func(w http.ResponseWriter, r *http.Request) {
		var c rescheduleCall
		json.NewDecoder(r.Body).Decode(&c)
		c.ID = r.PathValue("id")
		reschedules = append(reschedules, c)
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	r := &router{base: srv.URL, http: srv.Client(), tz: "UTC"}
	r.fireSplit(context.Background())

	if len(runlogs) != 1 || runlogs[0]["status"] != "error" {
		t.Errorf("runlogs = %+v want one error", runlogs)
	}
	if len(reschedules) != 1 || reschedules[0].Status != "active" {
		t.Errorf("reschedules = %+v want one active (restored)", reschedules)
	}
}
