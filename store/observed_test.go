package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
)

// observe puts a message and records it as ambient context for a folder — the
// pair the poll loop performs when a route resolves to `folder#observe`.
func observe(t *testing.T, s *Store, folder string, m core.Message) core.Message {
	t.Helper()
	if err := s.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkMessagesObserved(context.Background(), folder, []core.Message{m}); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestObservedTail_PromptIncludesBlock(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	folder := "rhias/nemo"
	now := time.Now()
	// One inbound from a chat under observe-mode routing.
	observe(t, s, folder, core.Message{
		ID: "obs-1", ChatJID: "telegram:99", Sender: "bob", Name: "bob",
		Content: "context line", Timestamp: now.Add(-time.Minute),
		Verb: "message", Source: "telegram",
	})

	observed, err := s.ObservedSince(context.Background(), folder, "", 10, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	observe(t, s, folder, core.Message{
		ID: "alza-1", ChatJID: jid, Sender: "user", Name: "user",
		Content: "https://example/dgx-spark", Timestamp: now.Add(-time.Minute),
		Verb: "message", Source: "discord",
	})

	// Trigger arrives in the same chat_jid: the observed message must
	// still appear in the tail (regression: used to be filtered out).
	observed, err := s.ObservedSince(context.Background(), folder, "", 10, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(observed) != 1 {
		t.Fatalf("observed from same JID as trigger len = %d, want 1", len(observed))
	}
	if observed[0].ID != "alza-1" {
		t.Errorf("got id=%q, want alza-1", observed[0].ID)
	}
}

// Per spec 5/F: two topics in same folder must both see ambient
// observed messages. Cursor on topic A doesn't starve topic B.
func TestObservedSince_PerTopicCursorIsolation(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	ctx := context.Background()
	folder := "main"
	now := time.Now().UTC()
	// One observed message at t=1.
	observe(t, s, folder, core.Message{
		ID: "o-1", ChatJID: "telegram:99", Sender: "bob", Name: "bob",
		Content: "ambient context", Timestamp: now.Add(-2 * time.Minute),
		Verb: "message", Source: "telegram",
	})

	// Topic A reads with cursor="" — sees the observed message.
	a, err := s.ObservedSince(ctx, folder, "", 10, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].ID != "o-1" {
		t.Fatalf("topic A read: got %v, want [o-1]", a)
	}

	// Simulate cursor advance for topic A.
	cursorA := core.TS(a[0].Timestamp)

	// Topic A reads again WITH cursor — must see nothing new.
	a2, err := s.ObservedSince(ctx, folder, cursorA, 10, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(a2) != 0 {
		t.Errorf("topic A re-read with cursor: got %d, want 0", len(a2))
	}

	// Topic B (different cursor — empty, fresh topic) — still sees o-1.
	b, err := s.ObservedSince(ctx, folder, "", 10, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
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
		observe(t, s, folder, core.Message{
			ID: id, ChatJID: "telegram:99", Sender: "bob", Name: "bob",
			Content: bigContent, Timestamp: now.Add(time.Duration(i) * time.Second),
			Verb: "message", Source: "telegram",
		})
	}

	// Char cap of 4000 must drop older messages (3000+3000>4000, so 1 fits).
	out, err := s.ObservedSince(context.Background(), folder, "", 10, 4000, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 {
		t.Fatalf("char-cap len = %d, want 1", len(out))
	}
	if out[0].ID != "c" {
		t.Errorf("char-cap kept = %s, want c (newest)", out[0].ID)
	}
}
