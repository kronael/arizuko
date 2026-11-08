package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

type failingTurnRead struct{ objstore.Store }

func (f failingTurnRead) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == TurnContexts {
		return nil, errors.New("turn read failed")
	}
	return f.Store.Query(ctx, q)
}

func TestActiveProgressReturnsContextReadError(t *testing.T) {
	s := openMem(t)
	if err := s.PutMessage(core.Message{ID: "status", ChatJID: "web:a", Topic: "t", TurnID: "turn", BotMsg: true, Verb: verbStatus, Content: "working", Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	failing := NewOver(failingTurnRead{Store: s.Obj()})
	if _, err := failing.ActiveProgress("web:a", "t", ""); err == nil {
		t.Fatal("turn read error was hidden")
	}
}

// A turn id is guessable and is not proof of ownership: TurnFrames must bind
// the turn to its chat. The HTTP face proved this separately (webd/turn.go
// authorizeTurn), the MCP face did not, so the same query served both a
// contained and an uncontained caller. Containment belongs in the query.
func TestTurnFrames_DoesNotCrossChats(t *testing.T) {
	s := openMem(t)

	put := func(id, jid, turnID string) {
		t.Helper()
		if err := s.PutMessage(core.Message{
			ID: id, ChatJID: jid, Sender: "bot", Name: "bot",
			Content: "frame " + id, Timestamp: time.Now(), TurnID: turnID,
			BotMsg: true,
		}); err != nil {
			t.Fatal(err)
		}
	}
	put("m-a", "web:tenant-a", "turn-a")
	put("m-b", "web:tenant-b", "turn-b")

	// Own turn: visible.
	own, err := s.TurnFrames("web:tenant-a", "turn-a", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(own) != 1 {
		t.Fatalf("own turn returned %d frames, want 1", len(own))
	}

	// Another tenant's turn id, with our own JID: nothing.
	foreign, err := s.TurnFrames("web:tenant-a", "turn-b", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(foreign) != 0 {
		t.Errorf("foreign turn returned %d frames, want 0 — turn id alone must not grant a read", len(foreign))
	}

	// An empty JID must match nothing rather than everything.
	none, err := s.TurnFrames("", "turn-a", "", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("empty chat_jid returned %d frames, want 0", len(none))
	}

	// The afterID paging branch carries the same predicate.
	page, err := s.TurnFrames("web:tenant-a", "turn-b", "m-b", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 0 {
		t.Errorf("paged foreign turn returned %d frames, want 0", len(page))
	}
}

func TestActiveProgressScopeLifecycleAndPageCapacity(t *testing.T) {
	s := openMem(t)
	jid, topic := "web:tenant-a", "thread"
	for _, turn := range []string{"live", "retry", "done", "expired"} {
		if _, err := s.OpenTurnContext(turn, "demo", topic, jid, "u", "", nil); err != nil {
			t.Fatal(err)
		}
	}
	for turn, state := range map[string]string{"retry": TurnPendingRetry, "done": TurnDone, "expired": TurnExpired} {
		if err := s.SetTurnState(turn, state); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().Add(-10 * time.Minute)
	put := func(id, turn, rowTopic, verb string, at time.Time) {
		t.Helper()
		if err := s.PutMessage(core.Message{ID: id, ChatJID: jid, Topic: rowTopic, TurnID: turn, BotMsg: true, Verb: verb, Content: id, Timestamp: at}); err != nil {
			t.Fatal(err)
		}
	}
	put("active", "live", topic, verbStatus, base)
	put("retry", "retry", topic, verbStatus, base.Add(time.Second))
	put("foreign-topic", "live", "other", verbStatus, base.Add(2*time.Second))
	for i := range 100 {
		put(fmt.Sprintf("normal-%03d", i), "", topic, "", base.Add(time.Duration(i+3)*time.Second))
	}
	for i := range 110 {
		put(fmt.Sprintf("dead-%03d", i), "done", topic, verbStatus, base.Add(time.Duration(i+120)*time.Second))
	}
	put("expired", "expired", topic, verbStatus, base.Add(240*time.Second))
	put("missing", "missing", topic, verbStatus, base.Add(241*time.Second))
	normal, err := s.MessagesByThread(jid, topic, time.Now(), 100)
	if err != nil || len(normal) != 100 {
		t.Fatalf("normal page %d: %v", len(normal), err)
	}
	active, err := s.ActiveProgress(jid, topic, "")
	if err != nil || len(active) != 2 {
		t.Fatalf("active: %+v %v", active, err)
	}
	foreign, err := s.ActiveProgress("web:other", topic, "live")
	if err != nil || len(foreign) != 0 {
		t.Fatalf("foreign: %+v %v", foreign, err)
	}
	if err := s.SetTurnState("live", TurnDone); err != nil {
		t.Fatal(err)
	}
	active, err = s.ActiveProgress(jid, "", "live")
	if err != nil || len(active) != 0 {
		t.Fatalf("ended: %+v %v", active, err)
	}
}

// A progress line is live scaffolding: a turn's replay never returns it, so a
// browser that reloads after the turn ended does not get the line back.
func TestTurnFrames_OmitsTheProgressLine(t *testing.T) {
	s := openMem(t)
	const jid = "web:tenant-a"
	now := time.Now()
	for i, row := range []core.Message{
		{ID: "line", Verb: verbStatus, Content: "✓ working"},
		{ID: "answer", Content: "the answer"},
	} {
		row.ChatJID, row.TurnID, row.BotMsg = jid, "turn-a", true
		row.Timestamp = now.Add(time.Duration(i) * time.Second)
		if err := s.PutMessage(row); err != nil {
			t.Fatal(err)
		}
	}
	frames, err := s.TurnFrames(jid, "turn-a", "", 200)
	if err != nil || len(frames) != 1 || frames[0].ID != "answer" {
		t.Fatalf("replay = %+v, err %v; want only the answer", frames, err)
	}
}
