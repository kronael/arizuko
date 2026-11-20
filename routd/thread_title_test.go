package routd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
)

func TestThreadTitleRefusalStatusAndRetry(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const jid = "discord:g1/ch-1"
	if err := db.SetChatIsGroup(jid, true); err != nil {
		t.Fatal(err)
	}
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	threadTurn(t, db, "t1", "demo", jid, "root-1")
	var mu sync.Mutex
	var requests []chanlib.SendRequest
	adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req chanlib.SendRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		requests = append(requests, req)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req.Title == "":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":"pass title to start a Discord thread"}`))
		case len(requests) == 3:
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"temporarily unavailable"}`))
		default:
			_, _ = w.Write([]byte(`{"id":"reply-ok"}`))
		}
	}))
	defer adapter.Close()
	reg := chanreg.New()
	if _, err := reg.Register("discord", adapter.URL, []string{"discord:"}, map[string]bool{"send_text": true}); err != nil {
		t.Fatal(err)
	}
	dl := NewChannelDeliverer(reg, nil, nil)
	srv := NewServer(db, nil, dl, nil, 0, "")
	ctx := context.Background()
	row := core.Message{ID: "status-title", ChatJID: jid, Topic: "root-1", TurnID: "t1", RoutedTo: "demo", Timestamp: time.Now(), BotMsg: true, FromMe: true, Verb: "status"}
	if err := db.store().QueueStatus(ctx, row, "working", "progress"); err != nil {
		t.Fatal(err)
	}
	status, ok, err := db.store().PendingDelivery(ctx, row)
	if err != nil || !ok {
		t.Fatalf("status not queued: %v %v", ok, err)
	}
	if err := executeStatus(db, dl, status); !errors.Is(err, chanlib.ErrPermanent) {
		t.Fatalf("status refusal is not permanent: %v", err)
	}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.maybeRetryOutbound(time.Now().Add(time.Hour))
	mu.Lock()
	statusCalls := len(requests)
	mu.Unlock()
	if statusCalls != 1 {
		t.Fatalf("refused status retried: calls=%d", statusCalls)
	}
	bound := turnMCP{folder: "demo", chatJID: jid, turnID: "t1", trigger: "u1"}
	sock := filepath.Join(t.TempDir(), "title.sock")
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound), srv.buildStoreFns(bound), "demo", true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	args := map[string]any{"chatJid": jid, "text": "unrelated reply words"}
	if _, message := callToolOverSock(t, sock, "reply", args); !strings.Contains(message, "pass title") ||
		strings.Contains(message, "do not send it again") {
		t.Fatalf("permanent refusal did not reach agent: %q", message)
	}
	args["title"] = "Chosen title with five whole words"
	if _, message := callToolOverSock(t, sock, "reply", args); !strings.Contains(message, "do not send it again") {
		t.Fatalf("transient failure not queued: %q", message)
	}
	pending, err := db.PendingOutbound(time.Now().Add(time.Hour), 100)
	if err != nil || len(pending) != 1 || pending[0].Title != args["title"] || pending[0].Topic != "root-1" {
		t.Fatalf("pending title and placement: %+v %v", pending, err)
	}
	loop.retryOutbound(time.Now().Add(time.Hour), pending[0])
	if pending, err := db.PendingOutbound(time.Now().Add(2*time.Hour), 100); err != nil || len(pending) != 0 {
		t.Fatalf("retry did not finish: %+v %v", pending, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 4 || requests[0].Title != "" || requests[0].ThreadRoot != "root-1" {
		t.Fatalf("status started a thread or retried: %+v", requests)
	}
	for _, req := range requests[2:] {
		if req.Title != args["title"] || req.ThreadRoot != "root-1" || req.ThreadID != "" {
			t.Fatalf("delivery lost title or placement: %+v", req)
		}
	}
	if requests[2].IdempotencyKey != requests[3].IdempotencyKey {
		t.Fatal("retry changed the delivery id")
	}
}
