package dynamo_test

// search_test.go is ranked search over the ENDPOINT. The index is memory, so
// what the server has to answer is the walk that fills it: `chats` for the
// partition list and one PROJECTED read per partition
// (store.MessagesToIndex) — a projection expression, over a served store, for
// every message an instance holds.
//
// The second body is the one that cannot pass by accident: a routd that
// appended nothing answers for messages another routd wrote, which it can only
// do by reading them back off the endpoint.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/routd/msgsearch"
)

// searched retries the warming refusal the way a caller does, and fails loud
// if the walk never finishes — an empty hit list must never stand in for one.
func searched(t *testing.T, d *routd.DB, query, scope string) []routd.FoundMessage {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		hits, err := d.FindMessages(query, scope, "", "", 10)
		switch {
		case errors.Is(err, msgsearch.ErrWarming):
			if time.Now().After(deadline) {
				t.Fatal("the ranked index never finished its walk of the endpoint")
			}
			time.Sleep(5 * time.Millisecond)
		case err != nil:
			t.Fatalf("FindMessages(%q): %v", query, err)
		default:
			return hits
		}
	}
}

// The messages a served routd appends answer a search, with the snippet its
// callers render and the folder stamp a subtree scope reads.
func TestSearchRunsOverDynamo(t *testing.T) {
	rdb := routd.OpenOver(routdHandle(t))
	defer rdb.Close() //nolint:errcheck // the test is ending
	// The index is this process's, and OpenOver hands back a routd that answers
	// no search until its owner starts one — the daemon's main does it, so a
	// test that searches does it too.
	rdb.StartSearch()

	now := time.Now()
	for i, m := range []struct{ id, jid, sender, folder, content string }{
		{"s1", "telegram:1", "alice", "acme/eng", "the Q3 budget meeting is tomorrow"},
		{"s2", "telegram:1", "bob", "acme/eng", "no budget for the offsite"},
		{"s3", "telegram:2", "alice", "other", "budget approved elsewhere"},
	} {
		if err := rdb.PutMessage(core.Message{
			ID: m.id, ChatJID: m.jid, Sender: m.sender, RoutedTo: m.folder,
			Content: m.content, Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("PutMessage over the endpoint: %v", err)
		}
	}

	hits := searched(t, rdb, "budget", "")
	if len(hits) != 3 {
		t.Fatalf("hits = %d, want the 3 messages that say budget", len(hits))
	}
	for _, h := range hits {
		if !strings.Contains(h.Content, "«budget»") {
			t.Errorf("hit in %s carries no mark: %q", h.ChatJID, h.Content)
		}
	}
	if hits := searched(t, rdb, `"budget meeting"`, ""); len(hits) != 1 {
		t.Errorf("the phrase answered %d hits, want the one message that holds it", len(hits))
	}
	if hits := searched(t, rdb, "budget", "acme"); len(hits) != 2 {
		t.Errorf("the folder subtree answered %d hits, want acme's 2", len(hits))
	}
	if hits := searched(t, rdb, "budget", "telegram:2"); len(hits) != 1 {
		t.Errorf("the chat scope answered %d hits, want 1", len(hits))
	}
}

// The boot walk reads the endpoint. This routd appends nothing: every message
// it answers for came back through the projected read, which is the half a
// served store has to serve.
func TestTheBootWalkReadsTheEndpoint(t *testing.T) {
	h := routdHandle(t)
	writer := routd.OpenOver(h)
	now := time.Now()
	for i := range 40 {
		content := "filler entry"
		// The flag is deliberately not the needle's own parity: every hit
		// carrying it would prove nothing about the projection.
		if i%4 == 0 {
			content = "needle in chat " + string(rune('a'+i%7))
		}
		if err := writer.PutMessage(core.Message{
			ID:        "w" + string(rune('a'+i%26)) + string(rune('0'+i/26)),
			ChatJID:   "telegram:" + string(rune('a'+i%7)),
			Sender:    "u",
			RoutedTo:  "acme/eng",
			Content:   content,
			FromMe:    i%8 == 0,
			Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("PutMessage: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	reader := routd.OpenOver(h)
	defer reader.Close() //nolint:errcheck // the test is ending
	reader.StartSearch()
	hits := searched(t, reader, "needle", "")
	t.Logf("a walk of 7 chats holding 40 messages, and one search, over the endpoint: %v",
		time.Since(start))
	if len(hits) != 10 {
		t.Fatalf("the walk found %d of the 10 messages that say needle", len(hits))
	}
	// The projection is what carries the sender and the two flags, so a
	// projection that dropped one would answer hits with an empty sender and
	// every flag false.
	for _, h := range hits {
		if h.Sender != "u" {
			t.Errorf("hit in %s came back with sender %q", h.ChatJID, h.Sender)
		}
	}
	flagged := 0
	for _, h := range hits {
		if h.IsFromMe {
			flagged++
		}
	}
	if flagged == 0 || flagged == len(hits) {
		t.Errorf("%d of %d hits carry is_from_me; the projection is not bringing the flag "+
			"back, it is inventing one answer for every row", flagged, len(hits))
	}
	if hits := searched(t, reader, "needle", "acme"); len(hits) != 10 {
		t.Errorf("the folder stamp did not survive the walk: %d hits", len(hits))
	}
}
