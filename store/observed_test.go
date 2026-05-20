package store

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
)

func TestObservedTail_PromptIncludesBlock(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	folder := "rhias/nemo"
	now := time.Now()
	// One inbound from a chat under observe-mode routing.
	if err := s.PutMessage(core.Message{
		ID: "obs-1", ChatJID: "telegram:99", Sender: "bob", Name: "bob",
		Content: "context line", Timestamp: now.Add(-time.Minute),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMessagesObserved(folder, []string{"obs-1"}); err != nil {
		t.Fatal(err)
	}

	observed := s.ObservedSince(folder, "", 10, 4000)
	if len(observed) != 1 {
		t.Fatalf("observed tail = %d, want 1", len(observed))
	}

	// Trigger turn rendering: msgs + observed -> <observed> tag present.
	trig := []core.Message{{
		ID: "trig-1", ChatJID: "telegram:1", Sender: "alice", Name: "alice",
		Content: "explicit ask", Timestamp: now, Verb: "message",
	}}
	rendered := router.FormatMessages(trig, observed)
	if !strings.Contains(rendered, "<observed") {
		t.Errorf("expected <observed> block in render, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "context line") {
		t.Errorf("expected observed content, got:\n%s", rendered)
	}

	// Absence: no observed messages -> no <observed> tag.
	empty := router.FormatMessages(trig, nil)
	if strings.Contains(empty, "<observed") {
		t.Errorf("expected no <observed> block, got:\n%s", empty)
	}
}

// Regression: a non-mention message in the SAME chat_jid as the next
// trigger used to get filtered out twice — past the cursor in the
// regular feed AND excluded from ObservedTail. Confirmed live on
// sloth (#general retold a link as observed, then a @mention turn
// in the same channel landed with the link content invisible).
func TestObservedTail_SameJIDAsTriggerSurfaces(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	folder := "main"
	now := time.Now()
	jid := "discord:837270552959516682/870660344358506566"
	if err := s.PutMessage(core.Message{
		ID: "alza-1", ChatJID: jid, Sender: "user", Name: "user",
		Content: "https://example/dgx-spark", Timestamp: now.Add(-time.Minute),
		Verb: "message", Source: "discord",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMessagesObserved(folder, []string{"alza-1"}); err != nil {
		t.Fatal(err)
	}

	// Trigger arrives in the same chat_jid: the observed message must
	// still appear in the tail (regression: used to be filtered out).
	observed := s.ObservedSince(folder, "", 10, 4000)
	if len(observed) != 1 {
		t.Fatalf("observed from same JID as trigger len = %d, want 1", len(observed))
	}
	if observed[0].ID != "alza-1" {
		t.Errorf("got id=%q, want alza-1", observed[0].ID)
	}
}

// Per spec 6/F: two topics in same folder must both see ambient
// observed messages. Cursor on topic A doesn't starve topic B.
func TestObservedSince_PerTopicCursorIsolation(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	folder := "main"
	now := time.Now().UTC()
	// One observed message at t=1.
	if err := s.PutMessage(core.Message{
		ID: "o-1", ChatJID: "telegram:99", Sender: "bob", Name: "bob",
		Content: "ambient context", Timestamp: now.Add(-2 * time.Minute),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMessagesObserved(folder, []string{"o-1"}); err != nil {
		t.Fatal(err)
	}

	// Topic A reads with cursor="" — sees the observed message.
	a := s.ObservedSince(folder, "", 10, 4000)
	if len(a) != 1 || a[0].ID != "o-1" {
		t.Fatalf("topic A read: got %v, want [o-1]", a)
	}

	// Simulate cursor advance for topic A.
	cursorA := a[0].Timestamp.UTC().Format(time.RFC3339Nano)

	// Topic A reads again WITH cursor — must see nothing new.
	a2 := s.ObservedSince(folder, cursorA, 10, 4000)
	if len(a2) != 0 {
		t.Errorf("topic A re-read with cursor: got %d, want 0", len(a2))
	}

	// Topic B (different cursor — empty, fresh topic) — still sees o-1.
	b := s.ObservedSince(folder, "", 10, 4000)
	if len(b) != 1 || b[0].ID != "o-1" {
		t.Errorf("topic B read: got %v, want [o-1]", b)
	}
}

func TestObservedTail_CharCap(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	folder := "rhias/nemo"
	now := time.Now()
	bigContent := strings.Repeat("x", 3000)
	for i, id := range []string{"a", "b", "c"} {
		if err := s.PutMessage(core.Message{
			ID: id, ChatJID: "telegram:99", Sender: "bob", Name: "bob",
			Content: bigContent, Timestamp: now.Add(time.Duration(i) * time.Second),
			Verb: "message", Source: "telegram",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.MarkMessagesObserved(folder, []string{"a", "b", "c"}); err != nil {
		t.Fatal(err)
	}

	// Char cap of 4000 must drop older messages (3000+3000>4000, so 1 fits).
	out := s.ObservedSince(folder, "", 10, 4000)
	if len(out) != 1 {
		t.Fatalf("char-cap len = %d, want 1", len(out))
	}
	if out[0].ID != "c" {
		t.Errorf("char-cap kept = %s, want c (newest)", out[0].ID)
	}
}

// Spec 6/F: open siblings' observed messages surface in this folder's
// ambient block. Closed siblings stay isolated.
func TestObservedSince_OpenSiblingsCrossFolder(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Two sibling folders both under "main".
	for _, f := range []string{"main/a", "main/b"} {
		if err := s.PutGroup(core.Group{Folder: f, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := s.PutMessage(core.Message{
		ID: "a-1", ChatJID: "telegram:1", Sender: "u", Name: "u",
		Content: "from-a", Timestamp: now.Add(-2 * time.Minute),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutMessage(core.Message{
		ID: "b-1", ChatJID: "telegram:2", Sender: "u", Name: "u",
		Content: "from-b", Timestamp: now.Add(-time.Minute),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMessagesObserved("main/a", []string{"a-1"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMessagesObserved("main/b", []string{"b-1"}); err != nil {
		t.Fatal(err)
	}

	// A reading (B still open) sees both.
	got := s.ObservedSince("main/a", "", 10, 4000)
	if len(got) != 2 {
		t.Fatalf("open siblings: got %d msgs, want 2 (%+v)", len(got), got)
	}

	// Close B; A now sees only its own.
	if err := s.SetGroupOpen("main/b", false); err != nil {
		t.Fatal(err)
	}
	got = s.ObservedSince("main/a", "", 10, 4000)
	if len(got) != 1 || got[0].ID != "a-1" {
		t.Fatalf("after closing B: got %+v, want [a-1]", got)
	}
}

// observe_group: observer sees primary-delivered messages from watched source
// as ambient context even though those messages are not is_observed=1.
func TestObservedSince_WatchedSources(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for _, f := range []string{"root", "child"} {
		if err := s.PutGroup(core.Group{Folder: f, AddedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	// Primary delivery to child (is_observed=0).
	if err := s.PutMessage(core.Message{
		ID: "c-1", ChatJID: "telegram:1", Sender: "u", Name: "u",
		Content: "child-msg", Timestamp: now.Add(-time.Minute),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(
		`UPDATE messages SET routed_to = 'child', is_observed = 0 WHERE id = 'c-1'`,
	); err != nil {
		t.Fatal(err)
	}

	// Without watching: root sees nothing.
	got := s.ObservedSince("root", "", 10, 4000)
	if len(got) != 0 {
		t.Fatalf("no watcher: got %d msgs, want 0", len(got))
	}

	// Register root as watcher of child.
	if err := s.AddGroupWatcher("root", "child"); err != nil {
		t.Fatal(err)
	}

	// Now root sees the child's primary-delivered message.
	got = s.ObservedSince("root", "", 10, 4000)
	if len(got) != 1 || got[0].ID != "c-1" {
		t.Fatalf("after watching: got %+v, want [c-1]", got)
	}

	// Unwatch: root no longer sees it.
	if err := s.RemoveGroupWatcher("root", "child"); err != nil {
		t.Fatal(err)
	}
	got = s.ObservedSince("root", "", 10, 4000)
	if len(got) != 0 {
		t.Fatalf("after unwatch: got %d msgs, want 0", len(got))
	}
}
