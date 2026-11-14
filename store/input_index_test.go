package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
)

func TestInputIndexReadsOnlyEligibleChats(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	for i := range 40 {
		m := putInput(t, s, fmt.Sprintf("chat:%d", i), "noise", now.Add(time.Hour), false)
		if i%2 == 0 {
			if err := s.AdvanceAgentCursor(m.ChatJID, MessageCursor(m)); err != nil {
				t.Fatal(err)
			}
		}
	}
	m := putInput(t, s, "chat:due", "one", now, false)
	c := &stateReadCounter{Store: s.Obj()}
	reader := NewOver(c)
	got, err := reader.InputCursorsBefore(t.Context(), MessageCursor(m))
	if _, ok := got[m.ChatJID]; err != nil || len(got) != 1 || !ok || c.rows != 2 {
		t.Fatalf("candidates=%v rows=%d err=%v", got, c.rows, err)
	}
	if err := s.AdvanceAgentCursor(m.ChatJID, MessageCursor(m)); err != nil {
		t.Fatal(err)
	}
	c.rows = 0
	got, err = reader.InputCursorsBefore(t.Context(), MessageCursor(m))
	if err != nil || len(got) != 0 || c.rows != 0 {
		t.Fatalf("consumed=%v rows=%d err=%v", got, c.rows, err)
	}
}
func TestInputIndexHandlesLongCursorTies(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	cursor := MsgStamp(core.TS(now), strings.Repeat("z", 993))
	key, _ := chatKey("chat:wide")
	if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: Chats, Key: key, Attrs: map[string]string{attrChatInputFirst: cursor, attrChatInputLast: cursor}}); err != nil {
		t.Fatal(err)
	}
	got, err := s.InputCursorsBefore(t.Context(), MsgStamp(core.TS(now), "a"))
	if err != nil || len(got) != 0 {
		t.Fatalf("later tie=%v err=%v", got, err)
	}
	got, err = s.InputCursorsBefore(t.Context(), cursor)
	if err != nil || len(got) != 1 {
		t.Fatalf("same tie=%v err=%v", got, err)
	}
}

type inputReadRace struct {
	objstore.Store
	hook func()
}

func (r *inputReadRace) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == Chats && q.Key.Op == objstore.ExactKey && r.hook != nil {
		hook := r.hook
		r.hook = nil
		hook()
	}
	return r.Store.Query(ctx, q)
}
func TestInputIndexChecksConsumedHydration(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	m := putInput(t, s, "chat:one", "one", time.Now(), false)
	r := &inputReadRace{Store: s.Obj(), hook: func() {
		if err := s.AdvanceAgentCursor(m.ChatJID, MessageCursor(m)); err != nil {
			t.Fatal(err)
		}
	}}
	got, err := NewOver(r).InputCursorsBefore(t.Context(), MessageCursor(m))
	if err != nil || len(got) != 0 {
		t.Fatalf("consumed hydration=%v err=%v", got, err)
	}
}
func TestLiveTurnIndexReadsOnlyFolder(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	for i := range 41 {
		folder := "other"
		if i == 0 {
			folder = "mine"
		}
		key, _ := turnKey(fmt.Sprint(i))
		if err := s.WriteEntities(t.Context(), nil, objstore.Write{Op: objstore.Put, Entity: TurnContexts, Key: key, Attrs: map[string]string{attrTurnState: TurnRunning, attrTurnFolder: folder}}); err != nil {
			t.Fatal(err)
		}
	}
	c := &stateReadCounter{Store: s.Obj()}
	turns, err := NewOver(c).LiveTurnsInFolder(t.Context(), "mine")
	if err != nil || len(turns) != 1 || turns[0].Folder != "mine" || c.rows != 2 {
		t.Fatalf("turns=%v rows=%d err=%v", turns, c.rows, err)
	}
}
func TestInputIndexMigrationRunsOnce(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	cursor := MsgStamp(core.TS(time.Now()), "legacy")
	key, _ := chatKey("chat:legacy")
	tk, _ := turnKey("legacy")
	ok, err := s.obj.GuardedBatch(t.Context(), nil, []objstore.Write{
		{Op: objstore.Put, Entity: Chats, Key: key, Attrs: map[string]string{attrChatInputFirst: cursor, attrChatInputLast: cursor}},
		{Op: objstore.Put, Entity: TurnContexts, Key: tk, Attrs: map[string]string{attrTurnState: TurnRunning, attrTurnFolder: "mine"}},
	})
	if err != nil || !ok {
		t.Fatalf("legacy %v %v", ok, err)
	}
	if err := s.MigrateStateIndexes(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got, err := s.InputCursorsBefore(t.Context(), cursor); err != nil || len(got) != 1 {
		t.Fatalf("migrated chat %v %v", got, err)
	}
	if got, err := s.LiveTurnsInFolder(t.Context(), "mine"); err != nil || len(got) != 1 {
		t.Fatalf("migrated turn %v %v", got, err)
	}
	c := &stateReadCounter{Store: s.Obj()}
	if err := NewOver(c).MigrateStateIndexes(t.Context()); err != nil || c.rows != 12 {
		t.Fatalf("repeat reads=%d err=%v", c.rows, err)
	}
}

type inputAppendRace struct {
	objstore.Store
	hook func()
}

func (r *inputAppendRace) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	rows, err := r.Store.Query(ctx, q)
	if q.Entity == Chats && q.Key.Op == objstore.ExactKey && r.hook != nil {
		hook := r.hook
		r.hook = nil
		hook()
	}
	return rows, err
}
func TestInputSpanKeepsConcurrentAppend(t *testing.T) {
	s := openMem(t)
	defer s.Close()
	now := time.Now()
	old := putInput(t, s, "chat:one", "old", now, false)
	if err := s.AdvanceAgentCursor(old.ChatJID, MessageCursor(old)); err != nil {
		t.Fatal(err)
	}
	var earlier core.Message
	race := &inputAppendRace{Store: s.Obj(), hook: func() {
		var err error
		earlier, err = s.AppendMessage(core.Message{ID: "earlier", ChatJID: old.ChatJID, Sender: "u", Content: "earlier", Timestamp: now.Add(time.Second)})
		if err != nil {
			t.Fatal(err)
		}
	}}
	later, err := NewOver(race).AppendMessage(core.Message{ID: "later", ChatJID: old.ChatJID, Sender: "u", Content: "later", Timestamp: now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.InputCursorsBefore(t.Context(), MessageCursor(earlier))
	if err != nil || len(got) != 1 {
		t.Fatalf("lost earlier input: %v %v", got, err)
	}
	if err := s.AdvanceAgentCursor(old.ChatJID, MessageCursor(earlier)); err != nil {
		t.Fatal(err)
	}
	got, err = s.InputCursorsBefore(t.Context(), MessageCursor(later))
	if err != nil || len(got) != 1 {
		t.Fatalf("lost later input: %v %v", got, err)
	}
}
