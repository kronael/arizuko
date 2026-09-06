package routd

// routd runs the live FTS5 search: `/v1/messages/search`, the agent's
// `find_messages` MCP tool and reads_http all land on DB.FindMessages
// (`routd/reads.go`). The grammar it depends on is not arizuko's — it is
// SQLite's FTS5 tokenizer, its query parser and two shadow-table triggers —
// so nothing here is provable by reading the Go.

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// The 0001 schema creates messages_fts and its triggers. A row inserted
// through PutMessage must reach the shadow table via the AI trigger, and the
// hit must carry the "«»" snippet the reader renders.
func TestFTSIndexesAMessageOnInsert(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "alice", Name: "alice",
		Content: "Q3 budget meeting tomorrow at noon", Timestamp: time.Now(),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}

	out, err := db.FindMessages("budget", "", "", "", 10)
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

// Phrase, OR, NOT and prefix are FTS5's own operators. If the query ever
// reached a LIKE instead, every one of these returns the wrong count.
func TestFTSQuerySyntaxIsFTS5(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	for i, m := range []struct{ id, content string }{
		{"a", "budget meeting tomorrow"},
		{"b", "plan for next quarter"},
		{"c", "budget review with finance"},
		{"d", "budgeting workshop"},
	} {
		if err := db.PutMessage(core.Message{
			ID: m.id, ChatJID: "tg:1", Sender: "u", Content: m.content,
			Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		name string
		q    string
		want int
	}{
		{"single token", "budget", 2},
		{"phrase match", `"budget meeting"`, 1},
		{"OR operator", "budget OR plan", 3},
		{"NOT operator", "budget NOT meeting", 1},
		{"prefix wildcard", "budg*", 3}, // budget(×2) + budgeting
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := db.FindMessages(c.q, "", "", "", 10)
			if err != nil {
				t.Fatalf("FindMessages(%q): %v", c.q, err)
			}
			if len(out) != c.want {
				got := make([]string, len(out))
				for i, o := range out {
					got[i] = o.Content
				}
				t.Errorf("hits for %q = %d (%v), want %d", c.q, len(out), got, c.want)
			}
		})
	}
}

// The tokenizer is unicode61 with remove_diacritics=2, so "uroven" must match
// "úroveň". A default tokenizer makes every accented language unsearchable.
func TestFTSFoldsDiacritics(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "cz", ChatJID: "tg:1", Sender: "u",
		Content: "nová úroveň zabezpečení", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	out, err := db.FindMessages("uroven", "", "", "", 10)
	if err != nil {
		t.Fatalf("FindMessages: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("diacritic-folded match: got %d hits, want 1", len(out))
	}
}

// scope is polymorphic — a value containing ':' is a chat_jid, anything else a
// routed_to folder subtree — and sender and since narrow on top of it. Each
// filter is a separate `? IS NULL OR` arm, so one wrong bind order silently
// widens or empties the result.
func TestFTSFiltersNarrowTheResult(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	for _, r := range []struct {
		id, jid, sender, content, folder string
		ts                               time.Time
	}{
		{"a1", "tg:1", "alice", "budget alpha", "atlas/eng", now.Add(-2 * time.Hour)},
		{"a2", "tg:1", "bob", "budget beta", "atlas/eng", now.Add(-1 * time.Hour)},
		{"a3", "tg:2", "alice", "budget gamma", "atlas/support", now},
		{"a4", "tg:3", "alice", "budget delta", "other", now},
	} {
		if err := db.PutMessage(core.Message{
			ID: r.id, ChatJID: r.jid, Sender: r.sender, Content: r.content,
			Timestamp: r.ts, RoutedTo: r.folder,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct {
		name, scope, sender, since string
		want                       int
	}{
		{"chat_jid scope", "tg:1", "", "", 2},
		{"folder subtree", "atlas", "", "", 3},
		{"exact folder", "atlas/eng", "", "", 2},
		{"sender", "", "alice", "", 3},
		{"since", "", "", now.Add(-90 * time.Minute).Format(time.RFC3339), 3},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, err := db.FindMessages("budget", c.scope, c.sender, c.since, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(out) != c.want {
				t.Errorf("hits = %d, want %d", len(out), c.want)
			}
		})
	}
}

// A folder name may legally hold `%`, `_` or a capital: isValidFolder rejects
// only `..`, `\`, NUL, empty and over-long segments and reserved names. Under a
// bare SQL LIKE all three widen the scope, so a tenant at `%` reads every other
// tenant's message content and a Coach/coach pair read each other — a pair a
// live instance HAS carried (BUGS L122). groupfolder.SQLSubtree is what closes
// it, and this is the search path's proof.
func TestFTSFolderScopeIsNotWidenedByLikeMetacharacters(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	for i, f := range []string{"a_b/one", "aXb/two", "coach/k", "Coach/private", "x/y"} {
		if err := db.PutMessage(core.Message{
			ID: f, ChatJID: "tg:" + f, Sender: "u", Content: "needle",
			Timestamp: now.Add(time.Duration(i) * time.Second), RoutedTo: f,
		}); err != nil {
			t.Fatal(err)
		}
	}

	for _, c := range []struct{ scope, leak string }{
		{"a_b", "aXb/two"},         // _ is a single-character wildcard
		{"%", "x/y"},               // % matches every folder with a separator
		{"coach", "Coach/private"}, // LIKE is case-insensitive for ASCII
	} {
		got, err := db.FindMessages("needle", c.scope, "", "", 50)
		if err != nil {
			t.Fatalf("scope %q: %v", c.scope, err)
		}
		for _, m := range got {
			if m.ChatJID == "tg:"+c.leak {
				t.Errorf("scope %q read %q's messages", c.scope, c.leak)
			}
		}
	}

	// The exact subtree still reads, or the fix would be a denial of service.
	got, err := db.FindMessages("needle", "a_b", "", "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChatJID != "tg:a_b/one" {
		t.Errorf("the folder's own subtree was lost: %+v", got)
	}
}

// The AD trigger must drop the shadow row with its source. Without it a
// deleted message keeps answering searches, content and all.
func TestFTSDropsTheRowWhenTheMessageIsDeleted(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "d1", ChatJID: "tg:err", Sender: "u",
		Content: "to be deleted soon", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	out, _ := db.FindMessages("deleted", "", "", "", 10)
	if len(out) != 1 {
		t.Fatalf("pre-delete hits = %d, want 1", len(out))
	}
	if _, err := db.SQL().Exec(`DELETE FROM messages WHERE id = 'd1'`); err != nil {
		t.Fatal(err)
	}
	out, _ = db.FindMessages("deleted", "", "", "", 10)
	if len(out) != 0 {
		t.Errorf("post-delete hits = %d, want 0", len(out))
	}
}

// A malformed query is the caller's fault and must reach the caller. FTS5
// returns a parse error; swallowing it renders an empty result set, which the
// agent reads as "no messages match" and reports as fact.
func TestFTSSurfacesAMalformedQuery(t *testing.T) {
	db := memDB(t)
	if _, err := db.FindMessages(`"unbalanced`, "", "", "", 10); err == nil {
		t.Fatal("expected an error for a malformed query, got nil")
	}
}

// The trigger path and the bm25 ranking must hold at volume, and every hit
// must carry its snippet — the reader renders that string directly.
func TestFTSRanksAndHighlightsAtVolume(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	for i := range 1000 {
		c := fmt.Sprintf("entry %d filler text", i)
		if i%50 == 0 {
			c += " keyword target"
		}
		if err := db.PutMessage(core.Message{
			ID: fmt.Sprintf("b-%04d", i), ChatJID: "tg:bulk", Sender: "u",
			Content: c, Timestamp: now.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}

	out, err := db.FindMessages("keyword", "", "", "", 200)
	if err != nil {
		t.Fatalf("bulk FindMessages: %v", err)
	}
	if len(out) != 20 { // 1000 / 50
		t.Errorf("bulk hits = %d, want 20", len(out))
	}
	for _, o := range out {
		if !strings.Contains(o.Content, "«keyword»") {
			t.Errorf("missing highlight: %q", o.Content)
		}
	}
}
