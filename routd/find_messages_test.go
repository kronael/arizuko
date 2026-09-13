package routd

// routd runs the live ranked search: `find_messages` over MCP, `GET
// /v1/messages/find` over REST and reads_http all land on DB.FindMessages
// (routd/search.go). What is proved here is the WIRING — the walk that fills
// the index from the store, the four writes that keep it in step, and the
// refusal that reaches the caller while the walk is running. The index's own
// behaviour is proved beside it (routd/msgsearch).

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd/msgsearch"
)

// searched is what a caller does with the warming refusal: retry until the boot
// walk is through. Every body below goes through it, so none of them can pass by
// reading a half-built index.
func searched(t *testing.T, d *DB, query, scope, sender, since string, limit int) []FoundMessage {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		hits, err := d.FindMessages(query, scope, sender, since, limit)
		switch {
		case errors.Is(err, msgsearch.ErrWarming):
			if time.Now().After(deadline) {
				t.Fatal("the ranked index never finished its boot walk")
			}
			time.Sleep(time.Millisecond)
		case err != nil:
			t.Fatalf("FindMessages(%q): %v", query, err)
		default:
			return hits
		}
	}
}

// The refusal must reach the caller through this door. A nil hit list with no
// error would be read as "no message says that" — by an agent, and by the REST
// caller's 200.
func TestFindMessagesPassesTheWarmingRefusalOn(t *testing.T) {
	d := &DB{search: msgsearch.New()}
	hits, err := d.FindMessages("budget", "", "", "", 10)
	if !errors.Is(err, msgsearch.ErrWarming) {
		t.Fatalf("FindMessages answered err=%v with %d hits; want the warming refusal",
			err, len(hits))
	}
}

// routd is the sole appender, so the append is the feed: a message written
// through PutMessage answers a search, with the snippet the callers render.
func TestAnAppendedMessageIsSearchable(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "alice", Name: "alice",
		Content: "Q3 budget meeting tomorrow at noon", Timestamp: time.Now(),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}
	hits := searched(t, db, "budget", "", "", "", 10)
	if len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	if hits[0].ChatJID != "tg:1" || hits[0].Sender != "alice" {
		t.Errorf("hit metadata wrong: %+v", hits[0])
	}
	if !strings.Contains(hits[0].Content, "«budget»") {
		t.Errorf("snippet carries no mark: %q", hits[0].Content)
	}
}

// The boot walk is what an instance comes up with, and it reads the store
// rather than anything routd remembers: a second routd over the same store
// answers for messages it never appended itself.
func TestTheBootWalkFillsTheIndexFromTheStore(t *testing.T) {
	db := memDB(t)
	for i, content := range []string{"budget alpha", "budget beta", "unrelated"} {
		if err := db.PutMessage(core.Message{
			ID: string(rune('a' + i)), ChatJID: "tg:1", Sender: "u", Content: content,
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	fresh := OpenOver(db.Obj())
	defer fresh.Close() //nolint:errcheck // the test is ending
	if hits := searched(t, fresh, "budget", "", "", "", 10); len(hits) != 2 {
		t.Errorf("the walk found %d of the 2 messages the store holds", len(hits))
	}
}

// Media enrichment rewrites a body in place, so the search must answer the
// words the agent can actually read in the transcript.
func TestEnrichmentRewritesWhatAnswers(t *testing.T) {
	db := memDB(t)
	m := core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "u", Timestamp: time.Now(),
		Content: "look at this https://example.test/a.jpg", Attachments: "https://example.test/a.jpg",
	}
	if err := db.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := db.EnrichMessage(m, "look at this <attachment>whiteboard photo</attachment>"); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "whiteboard", "", "", "", 10); len(hits) != 1 {
		t.Errorf("the enriched body does not answer: %d hits", len(hits))
	}
}

// An observe rule stamps `routed_to`, and that stamp is what a folder-subtree
// scope reads: an observed message the index still holds as unrouted is
// unfindable by the folder it was ingested into.
func TestObservingStampsTheFolderTheScopeReads(t *testing.T) {
	db := memDB(t)
	m := core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "u", Content: "ambient budget note",
		Timestamp: time.Now(),
	}
	if err := db.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "budget", "atlas", "", "", 10); len(hits) != 0 {
		t.Fatalf("an unrouted message answered a folder scope: %+v", hits)
	}
	if err := db.MarkMessagesObserved("atlas/eng", []core.Message{m}); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "budget", "atlas", "", "", 10); len(hits) != 1 {
		t.Errorf("the observed message is not in atlas's subtree: %d hits", len(hits))
	}
}

// The breaker's prune is the one path that deletes a message in production
// (routd/loop.go onCircuitBreakerOpen). A deleted message that goes on
// answering searches reports content nobody can read back.
func TestAPrunedMessageStopsAnswering(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "d1", ChatJID: "tg:err", Sender: "u", Errored: true,
		Content: "to be deleted soon", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "deleted", "", "", "", 10); len(hits) != 1 {
		t.Fatalf("pre-prune hits = %d, want 1", len(hits))
	}
	if err := db.DeleteErroredMessages("tg:err"); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "deleted", "", "", "", 10); len(hits) != 0 {
		t.Errorf("post-prune hits = %d, want 0", len(hits))
	}
}

// A hit is addressed by the message's own coordinate, so the chat it is
// reported under is the chat it was written in. When that binding slips, the
// ACL filter resolves the route of the WRONG chat (ipc.find_messages) and a
// folder that never saw the message is handed its snippet.
func TestAHitBelongsToTheChatItIsReportedUnder(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	for i, m := range []struct{ id, jid, mark string }{
		{"z1", "tg:zulu", "zulu"},
		{"a1", "tg:alpha", "alpha"},
		{"z2", "tg:zulu", "zulu"},
		{"a2", "tg:alpha", "alpha"},
	} {
		if err := db.PutMessage(core.Message{
			ID: m.id, ChatJID: m.jid, Sender: "u",
			Content:   "needle " + m.mark + " " + m.id,
			Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	hits := searched(t, db, "needle", "", "", "", 50)
	if len(hits) != 4 {
		t.Fatalf("hits = %d, want 4", len(hits))
	}
	for _, h := range hits {
		mark := strings.TrimPrefix(h.ChatJID, "tg:")
		if !strings.Contains(h.Content, mark) {
			t.Errorf("%s answered with another chat's snippet: %q", h.ChatJID, h.Content)
		}
	}
}
