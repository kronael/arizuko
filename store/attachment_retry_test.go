package store

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/retry"
)

func TestAttachmentRetryExactDueAndFence(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	a := core.Message{ID: "same", ChatJID: "one", Sender: "user", Content: "one", Timestamp: now, Attachments: "[]"}
	b := a
	b.ChatJID = "two"
	b.Content = "two"
	for _, m := range []core.Message{a, b} {
		if err := s.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	first, ok, err := s.ReadAttachmentRetry(ctx, a)
	if err != nil || !ok {
		t.Fatalf("read: %v %v", ok, err)
	}
	decision := retry.Next(0, retry.Failure{Kind: "transient"}, now, time.Time{})
	ok, err = s.DeferAttachments(ctx, first, decision, "")
	if err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	if rows, err := s.DueAttachments(ctx, now); err != nil || len(rows) != 0 {
		t.Fatalf("early due: %v %v", rows, err)
	}
	if rows, err := s.DueAttachments(ctx, decision.NextAt); err != nil || len(rows) != 1 || rows[0].Message.ChatJID != a.ChatJID {
		t.Fatalf("due: %v %v", rows, err)
	}
	if ok, err := s.CompleteAttachments(ctx, first, "stale"); err != nil || ok {
		t.Fatalf("stale commit: %v %v", ok, err)
	}
	current, ok, err := s.ReadAttachmentRetry(ctx, a)
	if err != nil || !ok {
		t.Fatal(err)
	}
	terminal := retry.Decision{Failures: 10, Stop: true}
	if ok, err := s.DeferAttachments(ctx, current, terminal, "unavailable"); err != nil || !ok {
		t.Fatalf("terminal: %v %v", ok, err)
	}
	if rows, err := s.DueAttachments(ctx, decision.NextAt.Add(time.Hour)); err != nil || len(rows) != 0 {
		t.Fatalf("terminal due: %v %v", rows, err)
	}
	pointers, err := s.Obj().Query(ctx, objstore.Query{Entity: objstore.IndexEntity, Partition: "attachments/due"})
	if err != nil || len(pointers) != 0 {
		t.Fatalf("terminal pointers: %v %v", pointers, err)
	}
	terminalState, _, err := s.ReadAttachmentRetry(ctx, a)
	if err != nil || terminalState.State != "failed" {
		t.Fatalf("terminal state: %+v %v", terminalState, err)
	}
	other, ok, err := s.ReadAttachmentRetry(ctx, b)
	if err != nil || !ok || other.Message.Content != "two" || other.State != "" {
		t.Fatalf("other chat: %+v %v", other, err)
	}
}

func TestAttachmentRetryArchive(t *testing.T) {
	src := openMem(t)
	defer src.Close()
	dst := openMem(t)
	defer dst.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	pending := core.Message{ID: "pending", ChatJID: "chat", Sender: "user", Content: "pending", Timestamp: now, Attachments: "[]"}
	failed := pending
	failed.ID = "failed"
	failed.Timestamp = now.Add(time.Second)
	for _, m := range []core.Message{pending, failed} {
		if err := src.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []core.Message{pending, failed} {
		snapshot, _, err := src.ReadAttachmentRetry(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		decision := retry.Decision{Failures: 1, NextAt: now.Add(time.Minute)}
		if m.ID == "failed" {
			decision = retry.Decision{Failures: 10, Stop: true}
		}
		if ok, err := src.DeferAttachments(ctx, snapshot, decision, "unavailable"); err != nil || !ok {
			t.Fatalf("defer: %v %v", ok, err)
		}
	}
	var buf bytes.Buffer
	if _, err := src.ExportMessagesJSONL(ctx, &buf); err != nil {
		t.Fatal(err)
	}
	if _, _, refused, err := dst.ImportMessagesJSONL(ctx, &buf, 0); err != nil || len(refused) != 0 {
		t.Fatalf("import: %v %v", refused, err)
	}
	got, _, err := dst.ReadAttachmentRetry(ctx, failed)
	if err != nil || got.State != "failed" || got.Failures != 10 || got.Message.Attachments != "[]" || got.Message.Content != "unavailable" {
		t.Fatalf("failed: %+v %v", got, err)
	}
	due, err := dst.DueAttachments(ctx, now.Add(2*time.Minute))
	if err != nil || len(due) != 1 || due[0].Message.ID != "pending" {
		t.Fatalf("due: %+v %v", due, err)
	}
}

func TestAttachmentRetryObservedRevision(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	m := core.Message{ID: "one", ChatJID: "chat", Sender: "user", Content: "input", Timestamp: now, Attachments: "[]"}
	if err := s.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	stale, _, err := s.ReadAttachmentRetry(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: Messages, Key: stale.Observed.Key, Attrs: map[string]string{"test_change": "1"}}); err != nil || !ok {
		t.Fatalf("update: %v %v", ok, err)
	}
	if ok, err := s.CompleteAttachments(ctx, stale, "stale"); err != nil || ok {
		t.Fatalf("revision: %v %v", ok, err)
	}
}

type attachmentOwnerRace struct {
	objstore.Store
	owner   *Store
	message core.Message
	key     objstore.Key
	fired   bool
	err     error
}

func (r *attachmentOwnerRace) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	rows, err := r.Store.Query(ctx, q)
	if q.Entity == objstore.IndexEntity && q.Partition == indexPartition("turns/chat", "chat") && !r.fired {
		r.fired = true
		admission := &TurnAdmission{InputLast: MessageCursor(r.message), InputKeys: []objstore.Key{r.key}}
		var ok bool
		ok, r.err = r.owner.OpenTurnContext("turn", "demo", "", r.message.ChatJID, r.message.Sender, "", admission)
		if r.err == nil && !ok {
			r.err = fmt.Errorf("turn acquisition refused")
		}
	}
	return rows, err
}

func TestAttachmentRetirementFencesNewOwner(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	msg := core.Message{ID: "owned", ChatJID: "chat", Sender: "user", Content: "input", Timestamp: now, Attachments: "[]"}
	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}
	if err := s.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _, err := s.ReadAttachmentRetry(ctx, msg)
	if err != nil {
		t.Fatal(err)
	}
	decision := retry.Decision{Failures: 1, NextAt: now.Add(-time.Second)}
	if ok, err := s.DeferAttachments(ctx, snapshot, decision, ""); err != nil || !ok {
		t.Fatalf("defer: %v %v", ok, err)
	}
	if err := s.SetAgentCursor(msg.ChatJID, MessageCursor(msg)); err != nil {
		t.Fatal(err)
	}
	race := &attachmentOwnerRace{Store: s.Obj(), owner: s, message: msg, key: snapshot.Observed.Key}
	if _, err := NewOver(race).DueAttachments(ctx, now); err != nil {
		t.Fatal(err)
	}
	if !race.fired || race.err != nil {
		t.Fatalf("acquire: %v %v", race.fired, race.err)
	}
	current, _, err := s.ReadAttachmentRetry(ctx, msg)
	if err != nil || current.State != "pending" {
		t.Fatalf("new owner lost attachment work: %+v %v", current, err)
	}
}

func TestAttachmentRetryDueKeepsSameIDAcrossChats(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	for _, chat := range []string{"one", "two"} {
		msg := core.Message{ID: "same", ChatJID: chat, Sender: "user", Timestamp: now, Attachments: "[]"}
		if err := s.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
		snap, _, err := s.ReadAttachmentRetry(ctx, msg)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.DeferAttachments(ctx, snap, retry.Decision{Failures: 1, NextAt: now}, ""); err != nil || !ok {
			t.Fatalf("defer: %v %v", ok, err)
		}
	}
	due, err := s.DueAttachments(ctx, now)
	if err != nil || len(due) != 2 || due[0].Message.ChatJID == due[1].Message.ChatJID {
		t.Fatalf("due: %+v %v", due, err)
	}
}

func TestAttachmentRetryBoundedAndRestart(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	var dueMsg core.Message
	for i := range 41 {
		msg := core.Message{ID: fmt.Sprintf("m%d", i), ChatJID: "chat", Sender: "user", Timestamp: now, Attachments: "[]"}
		stored, err := s.AppendMessage(msg)
		if err != nil {
			t.Fatal(err)
		}
		snap, found, err := s.ReadAttachmentRetry(ctx, msg)
		if err != nil || !found || snap.Observed.Key.Sort != MessageCursor(stored) {
			t.Fatalf("coordinate: %+v %v", snap, err)
		}
		next := now.Add(time.Hour)
		if i == 0 {
			next = now.Add(time.Minute)
			dueMsg = stored
		}
		if ok, err := s.DeferAttachments(ctx, snap, retry.Decision{Failures: 3, NextAt: next}, ""); err != nil || !ok {
			t.Fatalf("defer: %v %v", ok, err)
		}
	}
	reopened := NewOver(s.obj)
	work, _, err := reopened.ReadAttachmentRetry(ctx, dueMsg)
	if err != nil || work.Failures != 3 || !work.NextAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("reopen: %+v %v", work, err)
	}
	counter := &stateReadCounter{Store: s.obj}
	rows, err := NewOver(counter).DueAttachments(ctx, now.Add(2*time.Minute))
	if err != nil || len(rows) != 1 || rows[0].Message.ID != dueMsg.ID || counter.rows > 4 {
		t.Fatalf("due: rows=%+v selected=%d err=%v", rows, counter.rows, err)
	}
}

func TestAttachmentRetryObservedPayload(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	for _, attr := range []string{attrMsgContent, attrMsgAttachments} {
		msg := core.Message{ID: attr, ChatJID: "chat", Sender: "user", Content: "input", Timestamp: time.Now().UTC(), Attachments: "[]"}
		if err := s.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
		stale, _, err := s.ReadAttachmentRetry(ctx, msg)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: Messages, Key: stale.Observed.Key, Attrs: map[string]string{attr: "new"}}); err != nil || !ok {
			t.Fatalf("update: %v %v", ok, err)
		}
		latest, _, err := s.ReadAttachmentRetry(ctx, msg)
		if err != nil {
			t.Fatal(err)
		}
		stale.Observed.Attrs[objstore.IndexRevision] = latest.Observed.Attrs[objstore.IndexRevision]
		if ok, err := s.CompleteAttachments(ctx, stale, "stale"); err != nil || ok {
			t.Fatalf("%s stale: %v %v", attr, ok, err)
		}
	}
}

func TestAttachmentRetryRejectsPartialState(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	ctx := context.Background()
	msg := core.Message{ID: "partial", ChatJID: "chat", Timestamp: time.Now().UTC(), Attachments: "[]"}
	if err := s.PutMessage(msg); err != nil {
		t.Fatal(err)
	}
	key, err := messageKeyOf(msg)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := s.TryWriteEntities(ctx, nil, objstore.Write{Op: objstore.Update, Entity: Messages, Key: key, Attrs: map[string]string{attachmentFailures: "0"}}); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if _, _, err := s.ReadAttachmentRetry(ctx, msg); err == nil {
		t.Fatal("partial retry state was accepted")
	}
}
