package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

type progressInspectDeliverer struct {
	*recDeliverer
	db  *DB
	row core.Message
}

type progressRetryDeliverer struct {
	*recDeliverer
	started chan struct{}
	release chan struct{}
}

func (d *progressRetryDeliverer) Send(jid, text, replyTo, threadID, root, id string) (string, error) {
	select {
	case d.started <- struct{}{}:
	default:
	}
	<-d.release
	return d.recDeliverer.Send(jid, text, replyTo, threadID, root, id)
}

func (d *progressInspectDeliverer) Send(jid, text, replyTo, threadID, root, id string) (string, error) {
	d.row, _ = d.db.MessageByID(jid, id)
	return d.recDeliverer.Send(jid, text, replyTo, threadID, root, id)
}

func TestProgress_FirstSendIsStoredAndMarkedDelivered(t *testing.T) {
	db, _ := newProgressTurn(t, &recDeliverer{}, "")
	if _, err := db.PutTurnContext("t1", "demo", "tt", progressJID, "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.store().PutMessage(core.Message{ID: "future", ChatJID: progressJID, Content: "future", Timestamp: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	dl := &progressInspectDeliverer{recDeliverer: &recDeliverer{}, db: db}
	srv := NewServer(db, nil, dl, nil, 0, "")
	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	if dl.row.Verb != "status" || dl.row.TurnID != "t1" || dl.row.Topic != "tt" || dl.row.Content != "⏳ working" {
		t.Fatalf("row during send: %+v", dl.row)
	}
	row, ok := db.MessageByID(progressJID, dl.row.ID)
	if !ok || row.PlatformID == "" {
		t.Fatalf("delivered row: %+v", row)
	}
	pending, err := db.PendingOutbound(time.Now().Add(2*time.Hour), 100)
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending: %+v %v", pending, err)
	}
}

func TestProgress_RetryFinishesBeforeEnd(t *testing.T) {
	base := &recDeliverer{failSend: true}
	db, srv := newProgressTurn(t, base, "")
	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	base.failSend = false
	dl := &progressRetryDeliverer{recDeliverer: base, started: make(chan struct{}, 2), release: make(chan struct{})}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	done := make(chan struct{})
	go func() { loop.maybeRetryOutbound(time.Now().Add(time.Hour)); close(done) }()
	<-dl.started
	ended := make(chan struct{})
	go func() { finishTurn(db, dl, "t1", endDone); close(ended) }()
	select {
	case <-ended:
		close(dl.release)
		<-done
		t.Fatal("end passed retry")
	case <-time.After(200 * time.Millisecond):
	}
	close(dl.release)
	<-done
	<-ended
	if len(base.sends) != 2 || len(base.deletes) != 1 {
		t.Fatalf("sends=%+v deletes=%+v", base.sends, base.deletes)
	}
	loop.maybeRetryOutbound(time.Now().Add(2 * time.Hour))
	if len(base.sends) != 2 {
		t.Fatalf("late retry: %+v", base.sends)
	}
}

func TestProgress_FirstSendSerializesRetrySweep(t *testing.T) {
	base := &recDeliverer{}
	db, _ := newProgressTurn(t, base, "")
	dl := &progressRetryDeliverer{recDeliverer: base, started: make(chan struct{}, 2), release: make(chan struct{})}
	srv := NewServer(db, nil, dl, nil, 0, "")
	sent := make(chan struct{})
	go func() { _, _ = srv.submitStatus(snapshot("1", "working")); close(sent) }()
	<-dl.started
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	swept := make(chan struct{})
	go func() { loop.maybeRetryOutbound(time.Now().Add(time.Hour)); close(swept) }()
	select {
	case <-swept:
		close(dl.release)
		<-sent
		t.Fatal("sweep passed first send")
	case <-time.After(200 * time.Millisecond):
	}
	close(dl.release)
	<-sent
	<-swept
	if len(base.sends) != 1 {
		t.Fatalf("duplicate sends: %+v", base.sends)
	}
}

func TestProgress_StaleSweepAfterEndDoesNotSend(t *testing.T) {
	base := &recDeliverer{failSend: true}
	db, srv := newProgressTurn(t, base, "")
	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.PendingOutbound(time.Now().Add(time.Hour), 100)
	if err != nil || len(rows) != 1 {
		t.Fatalf("pending: %+v %v", rows, err)
	}
	finishTurn(db, base, "t1", endDone)
	base.failSend = false
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: base})
	if !loop.retryProgress(time.Now().Add(time.Hour), rows[0]) {
		t.Fatal("status not handled")
	}
	if len(base.sends) != 1 {
		t.Fatalf("late send: %+v", base.sends)
	}
}
