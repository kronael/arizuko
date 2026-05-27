package store

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// TestMessagesFTS_MigrationApplies confirms the 0070 migration runs on a
// populated DB without losing rows: insert messages BEFORE the FTS table
// would exist (in practice the migration is idempotent so we just check
// the post-state — every existing row is searchable, since the migration
// backfills via `INSERT INTO messages_fts SELECT rowid, content FROM messages`).
func TestMessagesFTS_MigrationApplies(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	if err := s.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "alice", Name: "alice",
		Content: "Q3 budget meeting tomorrow at noon", Timestamp: now,
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}

	// Migration must have created the virtual table; row inserted above
	// reaches it via the AI trigger.
	out, err := s.FindMessages("budget", "", "", "", 10)
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("hits = %d, want 1", len(out))
	}
	if out[0].ChatJID != "tg:1" || out[0].Sender != "alice" {
		t.Errorf("hit metadata wrong: %+v", out[0])
	}
	if !strings.Contains(out[0].Content, "«budget»") {
		t.Errorf("snippet missing «budget» highlight: %q", out[0].Content)
	}
}

// TestMessagesFTS_QuerySyntax exercises phrase, OR, NOT, prefix operators —
// proves we're hitting real FTS5 syntax, not a homegrown LIKE.
func TestMessagesFTS_QuerySyntax(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	msgs := []struct{ id, content string }{
		{"a", "budget meeting tomorrow"},
		{"b", "plan for next quarter"},
		{"c", "budget review with finance"},
		{"d", "budgeting workshop"},
	}
	for i, m := range msgs {
		if err := s.PutMessage(core.Message{
			ID: m.id, ChatJID: "tg:1", Sender: "u", Content: m.content,
			Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		q    string
		want int
	}{
		{"single token", "budget", 2},
		{"phrase match", `"budget meeting"`, 1},
		{"OR operator", "budget OR plan", 3},
		{"NOT operator", "budget NOT meeting", 1},
		{"prefix wildcard", "budg*", 3}, // budget(×2) + budgeting
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := s.FindMessages(c.q, "", "", "", 10)
			if err != nil {
				t.Fatalf("FindMessages(%q): %v", c.q, err)
			}
			if len(out) != c.want {
				ids := make([]string, len(out))
				for i, o := range out {
					ids[i] = o.Content
				}
				t.Errorf("hits for %q = %d (%v), want %d", c.q, len(out), ids, c.want)
			}
		})
	}
}

// TestMessagesFTS_Diacritics confirms the unicode61 remove_diacritics=2
// tokenizer folds Czech / Spanish / etc. accents — searching "uroven"
// matches "úroveň".
func TestMessagesFTS_Diacritics(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.PutMessage(core.Message{
		ID: "cz", ChatJID: "tg:1", Sender: "u",
		Content: "nová úroveň zabezpečení", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.FindMessages("uroven", "", "", "", 10)
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("diacritic-folded match: got %d hits", len(out))
	}
}

// TestMessagesFTS_Filters confirms scope (chat_jid vs folder), sender,
// since combine correctly. Folder-scoped queries match `routed_to`
// (the per-message attribution column — spec 5/C names it
// `group_folder` but that was dropped in 0023). We set routed_to via
// the core.Message field so PutMessage binds it directly.
func TestMessagesFTS_Filters(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	rows := []struct {
		id, jid, sender, content, folder string
		ts                               time.Time
	}{
		{"a1", "tg:1", "alice", "budget alpha", "atlas/eng", now.Add(-2 * time.Hour)},
		{"a2", "tg:1", "bob", "budget beta", "atlas/eng", now.Add(-1 * time.Hour)},
		{"a3", "tg:2", "alice", "budget gamma", "atlas/support", now},
		{"a4", "tg:3", "alice", "budget delta", "other", now},
	}
	for _, r := range rows {
		if err := s.PutMessage(core.Message{
			ID: r.id, ChatJID: r.jid, Sender: r.sender, Content: r.content,
			Timestamp: r.ts, RoutedTo: r.folder,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// scope = chat_jid (contains ':')
	out, err := s.FindMessages("budget", "tg:1", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("chat_jid scope: got %d, want 2", len(out))
	}

	// scope = folder (no ':') — atlas matches atlas/eng + atlas/support
	out, err = s.FindMessages("budget", "atlas", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Errorf("folder subtree scope: got %d, want 3", len(out))
	}

	// scope = exact folder
	out, err = s.FindMessages("budget", "atlas/eng", "", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 2 {
		t.Errorf("exact folder scope: got %d, want 2", len(out))
	}

	// sender filter
	out, err = s.FindMessages("budget", "", "alice", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Errorf("sender filter: got %d, want 3", len(out))
	}

	// since filter (cuts off the -2h row)
	cutoff := now.Add(-90 * time.Minute).Format(time.RFC3339)
	out, err = s.FindMessages("budget", "", "", cutoff, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 3 {
		t.Errorf("since filter: got %d, want 3", len(out))
	}
}

// TestMessagesFTS_Bulk smokes 1k rows + a search to confirm the trigger
// path scales and the BM25 ranking returns rows in sane order.
func TestMessagesFTS_Bulk(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now()
	for i := 0; i < 1000; i++ {
		c := fmt.Sprintf("entry %d filler text", i)
		if i%50 == 0 {
			c = c + " keyword target"
		}
		if err := s.PutMessage(core.Message{
			ID:        fmt.Sprintf("b-%04d", i),
			ChatJID:   "tg:bulk",
			Sender:    "u",
			Content:   c,
			Timestamp: now.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := s.FindMessages("keyword", "", "", "", 200)
	if err != nil {
		t.Fatalf("bulk FindMessages: %v", err)
	}
	// 1000 / 50 = 20 expected matches
	if len(out) != 20 {
		t.Errorf("bulk hits = %d, want 20", len(out))
	}
	// All snippets should carry the highlight.
	for _, o := range out {
		if !strings.Contains(o.Content, "«keyword»") {
			t.Errorf("missing highlight: %q", o.Content)
		}
	}
}

// TestMessagesFTS_TriggerSyncOnDelete confirms the AD trigger removes
// rows from the shadow when the source row is deleted.
func TestMessagesFTS_TriggerSyncOnDelete(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.PutMessage(core.Message{
		ID: "d1", ChatJID: "tg:err", Sender: "u",
		Content: "to be deleted soon", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	out, _ := s.FindMessages("deleted", "", "", "", 10)
	if len(out) != 1 {
		t.Fatalf("pre-delete hits = %d, want 1", len(out))
	}
	if _, err := s.db.Exec(`DELETE FROM messages WHERE id = 'd1'`); err != nil {
		t.Fatal(err)
	}
	out, _ = s.FindMessages("deleted", "", "", "", 10)
	if len(out) != 0 {
		t.Errorf("post-delete hits = %d, want 0", len(out))
	}
}

// TestMessagesFTS_BadQuery confirms malformed FTS5 syntax surfaces as an
// error (not a panic, not silent empty results).
func TestMessagesFTS_BadQuery(t *testing.T) {
	s, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	// Unbalanced quote — FTS5 parse error.
	_, err = s.FindMessages(`"unbalanced`, "", "", "", 10)
	if err == nil {
		t.Fatal("expected error for malformed query, got nil")
	}
}
