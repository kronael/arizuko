package routd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/kronael/arizuko/ipc"
)

func snapshot(call string, labels ...string) ipc.TurnStatus {
	var todos []string
	for _, label := range labels {
		todos = append(todos, fmt.Sprintf(`{"content":%q,"status":"pending"}`, label))
	}
	return ipc.TurnStatus{TurnID: "t1", Source: "claude", Snapshot: []byte(fmt.Sprintf(`{"hook_event_name":"PostToolUse","tool_name":"TodoWrite","session_id":"s","tool_use_id":%q,"tool_input":{"todos":[%s]}}`, call, strings.Join(todos, ",")))}
}

func TestProgress_BoundedSafeChecklist(t *testing.T) {
	active := strings.Repeat("界", 1500) + " @everyone *bold*\nnext\u202e"
	progress := &turnProgress{sources: map[string][]progressItem{"claude": {
		{Content: "first", Status: "pending"},
		{Content: active, Status: "in_progress"},
		{Content: "last", Status: "pending"},
	}}}
	text := renderChecklist(progress)
	if len(progressMark+text) > 1999 || !utf8.ValidString(text) || !strings.Contains(text, "⏳ 界") || !strings.Contains(text, "tasks omitted") {
		t.Fatalf("bad bounded render: %d %q", len(progressMark+text), text)
	}
	safe := safeProgressText("@everyone *bold* _italic_ `code`\nnext\u202e")
	if strings.ContainsAny(safe, "@*_`\n\u202e") {
		t.Fatalf("unsafe: %q", safe)
	}
	if len(progressMark+boundProgress(safeProgressText(strings.Repeat("界", 1000)))) > 1999 {
		t.Fatal("fallback exceeds wire limit")
	}
}

func TestProgress_ChecklistSanitizesSendAndEdit(t *testing.T) {
	dl := &recDeliverer{}
	_, srv := newProgressTurn(t, dl, "")
	if _, err := srv.submitStatus(snapshot("1", "@everyone *bold*\nnext")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.submitStatus(snapshot("2", "@here _italic_ `code`")); err != nil {
		t.Fatal(err)
	}
	srv.flushProgress(time.Now().Add(progressInterval))
	if len(dl.sends) != 1 || len(dl.edits) != 1 {
		t.Fatalf("sends=%+v edits=%+v", dl.sends, dl.edits)
	}
	for _, text := range []string{dl.sends[0].text, dl.edits[0].content} {
		if strings.ContainsAny(text, "@*_`\n") {
			t.Fatalf("unsafe delivered text: %q", text)
		}
	}
}

func TestProgress_CoalescesLatestAndRetries(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	for _, event := range []ipc.TurnStatus{snapshot("1", "A"), snapshot("2", "B"), snapshot("3", "A")} {
		if _, err := srv.submitStatus(event); err != nil {
			t.Fatal(err)
		}
	}
	srv.flushProgress(time.Now().Add(progressInterval))
	if len(dl.sends) != 1 || len(dl.edits) != 0 {
		t.Fatalf("A B A: %+v %+v", dl.sends, dl.edits)
	}
	for _, event := range []ipc.TurnStatus{snapshot("4", "B"), snapshot("5", "C")} {
		if _, err := srv.submitStatus(event); err != nil {
			t.Fatal(err)
		}
	}
	dl.editErr = errors.New("edit failed")
	srv.flushProgress(time.Now().Add(progressInterval))
	if rows := statusRows(t, db, progressJID); !strings.Contains(rows[0].Content, "A") {
		t.Fatalf("failed edit changed row: %+v", rows)
	}
	dl.editErr = nil
	srv.flushProgress(time.Now().Add(2 * progressInterval))
	if rows := statusRows(t, db, progressJID); !strings.Contains(rows[0].Content, "C") || strings.Contains(rows[0].Content, "B") {
		t.Fatalf("latest not delivered: %+v", rows)
	}
	if _, err := srv.submitStatus(snapshot("6")); err != nil {
		t.Fatal(err)
	}
	srv.flushProgress(time.Now().Add(3 * progressInterval))
	if rows := statusRows(t, db, progressJID); !strings.Contains(rows[0].Content, "No remaining tasks.") {
		t.Fatalf("clear: %+v", rows)
	}
}

func TestProgress_FailedEditWaitsBeforeRetry(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "  @all *first*  "}); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(dl.sends[0].text, "@*") {
		t.Fatalf("unsafe send: %q", dl.sends[0].text)
	}
	value, _ := db.progress.Load("t1")
	progress := value.(*turnProgress)
	progress.lastPublished = time.Now().Add(-progressInterval)
	progress.lastAttempt = progress.lastPublished
	dl.editErr = errors.New("edit failed")
	if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "second"}); err == nil {
		t.Fatal("edit error not returned")
	}
	attempt := progress.lastAttempt
	srv.flushProgress(attempt.Add(time.Second))
	if progress.lastAttempt != attempt {
		t.Fatal("retried before interval")
	}
	dl.editErr = nil
	srv.flushProgress(attempt.Add(progressInterval))
	if len(dl.edits) != 2 || dl.edits[1].content != progressMark+"second" {
		t.Fatalf("retry: %+v", dl.edits)
	}
}

func TestProgress_UnsentRowKeepsLatestText(t *testing.T) {
	dl := &recDeliverer{failSend: true}
	db, srv := newProgressTurn(t, dl, "")
	if _, err := srv.submitStatus(snapshot("1", "old")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.submitStatus(snapshot("2", "new")); err != nil {
		t.Fatal(err)
	}
	rows := statusRows(t, db, progressJID)
	if len(rows) != 1 || rows[0].PlatformID != "" || !strings.Contains(rows[0].Content, "new") {
		t.Fatalf("unsent row: %+v", rows)
	}
	value, _ := db.progress.Load("t1")
	if !value.(*turnProgress).lastPublished.IsZero() {
		t.Fatal("failed send marked published")
	}
}

func TestProgress_ChecklistActivationReplacesFallbackImmediately(t *testing.T) {
	dl := &recDeliverer{}
	_, srv := newProgressTurn(t, dl, "")
	if _, err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "  preparing  "}); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.submitStatus(snapshot("1", "only task")); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 1 || len(dl.edits) != 1 || !strings.Contains(dl.edits[0].content, "only task") {
		t.Fatalf("activation: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
}

type progressSignalDeliverer struct {
	*recDeliverer
	edited chan struct{}
	block  chan struct{}
}

func (d *progressSignalDeliverer) Edit(jid, id, content, thread string) error {
	if d.edited != nil {
		d.edited <- struct{}{}
	}
	if d.block != nil {
		<-d.block
	}
	return d.recDeliverer.Edit(jid, id, content, thread)
}

func TestProgress_LoopFlushesAndEndCancels(t *testing.T) {
	base := &recDeliverer{}
	db, _ := newProgressTurn(t, base, "")
	dl := &progressSignalDeliverer{recDeliverer: base, edited: make(chan struct{}, 1)}
	srv := NewServer(db, nil, dl, nil, 0, "")
	if _, err := srv.submitStatus(snapshot("1", "A")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.submitStatus(snapshot("2", "B")); err != nil {
		t.Fatal(err)
	}
	value, _ := db.progress.Load("t1")
	value.(*turnProgress).lastPublished = time.Now().Add(-progressInterval)
	value.(*turnProgress).lastAttempt = time.Now().Add(-progressInterval)
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl, PollEvery: 10 * time.Millisecond})
	loop.BindServer(srv)
	ctx, cancel := context.WithCancel(context.Background())
	go loop.Run(ctx)
	select {
	case <-dl.edited:
	case <-time.After(5 * time.Second):
		t.Fatal("loop did not flush")
	}
	cancel()
	if !loop.Wait(5 * time.Second) {
		t.Fatal("loop did not stop")
	}
	if _, err := srv.submitStatus(snapshot("3", "C")); err != nil {
		t.Fatal(err)
	}
	finishTurn(db, dl, "t1", endDone)
	srv.flushProgress(time.Now().Add(time.Hour))
	if len(base.edits) != 1 {
		t.Fatalf("end flushed pending text: %+v", base.edits)
	}
}

func TestProgress_EndWaitsForFlush(t *testing.T) {
	base := &recDeliverer{}
	db, _ := newProgressTurn(t, base, "")
	dl := &progressSignalDeliverer{recDeliverer: base, edited: make(chan struct{}, 1), block: make(chan struct{})}
	srv := NewServer(db, nil, dl, nil, 0, "")
	if _, err := srv.submitStatus(snapshot("1", "A")); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.submitStatus(snapshot("2", "B")); err != nil {
		t.Fatal(err)
	}
	flushed := make(chan struct{})
	go func() { srv.flushProgress(time.Now().Add(progressInterval)); close(flushed) }()
	<-dl.edited
	ended := make(chan struct{})
	started := make(chan struct{})
	go func() { close(started); finishTurn(db, dl, "t1", endDone); close(ended) }()
	<-started
	select {
	case <-ended:
		close(dl.block)
		<-flushed
		t.Fatal("end passed the blocked edit")
	case <-time.After(200 * time.Millisecond):
	}
	close(dl.block)
	<-flushed
	<-ended
	srv.flushProgress(time.Now().Add(time.Hour))
	if len(base.edits) != 1 || len(base.deletes) != 1 {
		t.Fatalf("edits=%+v deletes=%+v", base.edits, base.deletes)
	}
}
