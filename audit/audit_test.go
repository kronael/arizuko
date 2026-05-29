package audit

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestNoopWhenDisabled(t *testing.T) {
	a := New(Config{Enabled: false})
	// Must not panic or write any file.
	a.EmitSystem(SystemEvent{Tool: "register_group", ActorSub: "user:1"})
	a.EmitWeb(WebEvent{Method: "GET", Path: "/api/foo", Status: 200})
}

func TestEmitSystem(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{
		Enabled:     true,
		DataDir:     dir,
		Instance:    "test",
		MaxBytes:    10 * 1024 * 1024,
		RotateHours: 24,
	})

	a.EmitSystem(SystemEvent{
		ActorSub: "telegram:user/1",
		Tool:     "register_group",
		Folder:   "corp/eng",
		Params:   map[string]any{"jid": "telegram:group/99"},
		Outcome:  Outcome{Status: "ok"},
	})

	raw, err := os.ReadFile(filepath.Join(dir, "audit-system.jl"))
	if err != nil {
		t.Fatal(err)
	}
	var ev SystemEvent
	if err := json.Unmarshal(raw[:len(raw)-1], &ev); err != nil {
		t.Fatalf("parse: %v\nraw: %s", err, raw)
	}
	if ev.Stream != "system" {
		t.Errorf("stream=%q want system", ev.Stream)
	}
	if ev.Instance != "test" {
		t.Errorf("instance=%q want test", ev.Instance)
	}
	if ev.Tool != "register_group" {
		t.Errorf("tool=%q", ev.Tool)
	}
	if ev.ID == "" {
		t.Error("id should be set")
	}
	if ev.TS == "" {
		t.Error("ts should be set")
	}
}

func TestEmitWeb(t *testing.T) {
	dir := t.TempDir()
	a := New(Config{
		Enabled:     true,
		DataDir:     dir,
		Instance:    "test",
		MaxBytes:    10 * 1024 * 1024,
		RotateHours: 24,
	})

	a.EmitWeb(WebEvent{
		TS:        time.Now().UTC().Format(time.RFC3339Nano),
		Method:    "POST",
		Path:      "/api/chat/abc",
		Status:    200,
		LatencyMS: 47,
		ActorSub:  "telegram:user/1",
		IP:        "1.2.3.4",
	})

	raw, err := os.ReadFile(filepath.Join(dir, "audit-web.jl"))
	if err != nil {
		t.Fatal(err)
	}
	var ev WebEvent
	if err := json.Unmarshal(raw[:len(raw)-1], &ev); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.Stream != "web" {
		t.Errorf("stream=%q want web", ev.Stream)
	}
	if ev.Status != 200 {
		t.Errorf("status=%d", ev.Status)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-system.jl")
	// 1-byte max triggers rotation after first write.
	w := newWriter(path, 1, 0, "", "")

	e1 := SystemEvent{ID: "1", TS: "t", Stream: "system", Instance: "x",
		Tool: "t", Outcome: Outcome{Status: "ok"}}
	e2 := e1
	e2.ID = "2"

	w.writeImmediate(e1)
	w.writeImmediate(e2)

	files, _ := filepath.Glob(path + ".*")
	if len(files) < 1 {
		t.Error("expected at least one rotated file")
	}
}

func TestEmitWebFlushesWebhook(t *testing.T) {
	var got int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&got, 1)
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	a := New(Config{
		Enabled: true, DataDir: dir, Instance: "test",
		MaxBytes: 10 * 1024 * 1024, RotateHours: 24,
		WebhookURL: srv.URL,
	})
	// Backdate lastFlush so the first write crosses the 5s threshold and posts.
	a.web.mu.Lock()
	a.web.lastFlush = time.Now().Add(-time.Minute)
	a.web.mu.Unlock()

	a.EmitWeb(WebEvent{Method: "GET", Path: "/x", Status: 200})

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&got) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&got) == 0 {
		t.Fatal("EmitWeb did not flush the web webhook")
	}
}

func TestCursorPersist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit-cursor.json")

	c := loadCursor(path)
	c.set("messages", 42)
	c.save()

	c2 := loadCursor(path)
	if got := c2.get("messages"); got != 42 {
		t.Errorf("cursor=%d want 42", got)
	}
}
