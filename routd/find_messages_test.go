package routd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/retry"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func searched(t *testing.T, d *DB, query, scope, sender, since string, limit int) []apiv1.FoundMessage {
	t.Helper()
	found, err := d.FindMessages(t.Context(), apiv1.FindRequest{
		Query: query, Scope: scope, Sender: sender, Since: since, Limit: limit,
	})
	if err != nil {
		t.Fatalf("FindMessages(%q): %v", query, err)
	}
	if !found.Complete && len(found.Messages) < limit {
		t.Fatalf("FindMessages(%q) stopped at %s with %d of %d hits", query,
			found.SearchedTo, len(found.Messages), limit)
	}
	return found.Messages
}

// An outbound row is found at the instant the store gave it, which is not
// the caller's when the caller's would bury the row.
func TestOutboundMessagesAreSearchableAtStoredTimestamp(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(fmt.Sprint(failure), func(t *testing.T) {
			db := memDB(t)
			now := time.Now().UTC()
			if err := db.PutMessage(core.Message{ID: "newer", ChatJID: "web:demo",
				Content: "newer", Timestamp: now}); err != nil {
				t.Fatal(err)
			}
			msg := core.Message{ID: "reply", ChatJID: "web:demo", Content: "outboundneedle",
				Timestamp: now.Add(-time.Hour), BotMsg: true}
			var err error
			if failure {
				_, err = db.PutOutboundFailure(msg, retry.Failure{Kind: "transient"})
			} else {
				err = db.PutOutbound(msg, core.MessageStatusSent)
			}
			if err != nil {
				t.Fatal(err)
			}
			stored, ok := db.MessageByID(msg.ChatJID, msg.ID)
			if !ok || !stored.Timestamp.After(msg.Timestamp) {
				t.Fatalf("stored row = %+v, found = %t", stored, ok)
			}
			hits := searched(t, db, "outboundneedle", "web:demo", "", "", 10)
			want := stored.Timestamp.UTC().Format(time.RFC3339Nano)
			if len(hits) != 1 || hits[0].Timestamp != want || !hits[0].IsBotMessage {
				t.Fatalf("hits = %+v, stored timestamp = %s", hits, want)
			}
		})
	}
}

func TestAnAppendedMessageIsSearchable(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "m1", ChatJID: "tg:1", Sender: "alice", Name: "alice",
		Content: "Q3 budget meeting tomorrow at noon", Timestamp: time.Now(),
		Verb: "message", Source: "telegram",
	}); err != nil {
		t.Fatal(err)
	}
	hits := searched(t, db, "budget", "tg:1", "", "", 10)
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

// A search holds no state: a second handle on the same store writes a message
// and the first handle finds it, with no step between the write and the search.
func TestASearchReadsWhatAnotherHandleWrote(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "a", ChatJID: "tg:1", Sender: "u", Content: "budget alpha", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "budget", "tg:1", "", "", 10); len(hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(hits))
	}
	other := OpenOver(db.Obj())
	if err := other.PutMessage(core.Message{
		ID: "b", ChatJID: "tg:1", Sender: "u", Content: "budget beta",
		Timestamp: time.Now().Add(time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	hits := searched(t, db, "budget", "tg:1", "", "", 10)
	if len(hits) != 2 || !strings.Contains(hits[0].Content, "beta") {
		t.Errorf("hits = %+v, want the other handle's message first and then alpha", hits)
	}
	if hits := searched(t, other, "budget", "tg:1", "", "", 10); len(hits) != 2 {
		t.Errorf("the second handle found %d of the 2 messages the store holds", len(hits))
	}
}

// Media enrichment rewrites a body in place, so the search must answer the
// words the agent can read in the transcript.
func TestEnrichmentRewritesWhatAnswers(t *testing.T) {
	db, loop, _ := enrichLoop(t, mediaConfig{Enabled: true, MaxBytes: 1024})
	m := core.Message{ID: "m1", ChatJID: "tg:1", Sender: "u", Timestamp: time.Now(), Content: "look at this", Attachments: `[{"data":"ZmlsZQ==","filename":"whiteboard.png","mime":"image/png"}]`}
	if err := db.PutMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := loop.enrichAttachments(context.Background(), &m, "demo", true); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "whiteboard", "tg:1", "", "", 10); len(hits) != 1 {
		t.Errorf("the enriched body does not answer: %d hits", len(hits))
	}
}

// An observe rule stamps `routed_to` and writes the folder feed, and the feed
// is what a folder scope reads.
func TestObservingPutsTheMessageInTheFolderScope(t *testing.T) {
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
	if hits := searched(t, db, "budget", "", "", "", 10); len(hits) != 0 {
		t.Fatalf("an unrouted message answered the scope of every folder: %+v", hits)
	}
	if err := db.MarkMessagesObserved("atlas/eng", []core.Message{m}); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "budget", "atlas", "", "", 10); len(hits) != 1 {
		t.Errorf("the observed message is not in atlas's subtree: %d hits", len(hits))
	}
	if hits := searched(t, db, "budget", "atl", "", "", 10); len(hits) != 0 {
		t.Errorf("the folder atl is not above atlas/eng and answered %d hits", len(hits))
	}
}

func TestAPrunedMessageStopsAnswering(t *testing.T) {
	db := memDB(t)
	if err := db.PutMessage(core.Message{
		ID: "d1", ChatJID: "tg:err", Sender: "u", Errored: true,
		Content: "to be deleted soon", Timestamp: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "deleted", "tg:err", "", "", 10); len(hits) != 1 {
		t.Fatalf("pre-prune hits = %d, want 1", len(hits))
	}
	if err := db.DeleteErroredMessages("tg:err"); err != nil {
		t.Fatal(err)
	}
	if hits := searched(t, db, "deleted", "tg:err", "", "", 10); len(hits) != 0 {
		t.Errorf("post-prune hits = %d, want 0", len(hits))
	}
}

// A folder scope merges the chats that route into the subtree, newest first,
// and each hit is reported under the chat it was written in.
func TestAFolderScopeMergesItsChatsNewestFirst(t *testing.T) {
	db := memDB(t)
	now := time.Now()
	for i, m := range []struct{ id, jid, folder string }{
		{"z1", "tg:zulu", "world/z"},
		{"a1", "tg:alpha", "world/a"},
		{"x1", "tg:xray", "other"},
		{"z2", "tg:zulu", "world/z"},
		{"a2", "tg:alpha", "world/a"},
	} {
		if err := db.PutMessage(core.Message{
			ID: m.id, ChatJID: m.jid, Sender: "u", RoutedTo: m.folder,
			Content:   "needle " + strings.TrimPrefix(m.jid, "tg:") + " " + m.id,
			Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	hits := searched(t, db, "needle", "world", "", "", 50)
	var order []string
	for _, h := range hits {
		if !strings.Contains(h.Content, strings.TrimPrefix(h.ChatJID, "tg:")) {
			t.Errorf("%s answered with another chat's snippet: %q", h.ChatJID, h.Content)
		}
		order = append(order, h.Content[strings.LastIndex(h.Content, " ")+1:])
	}
	if got := strings.Join(order, " "); got != "a2 z2 a1 z1" {
		t.Errorf("world answered %q, want a2 z2 a1 z1", got)
	}
}

// An incomplete answer continues from searched_to, over the store, and the
// calls together return each hit one time.
func TestAnIncompleteSearchContinuesFromSearchedTo(t *testing.T) {
	db := memDB(t)
	now := time.Now().Add(-time.Hour)
	for i := range 7 {
		if err := db.PutMessage(core.Message{
			ID: fmt.Sprint("m", i), ChatJID: "tg:1", Sender: "u", RoutedTo: "world",
			Content: fmt.Sprint("needle ", i), Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []string{"tg:1", "world"} {
		var got []string
		req := apiv1.FindRequest{Query: "needle", Scope: scope, Limit: 3}
		for call := 0; ; call++ {
			if call > 7 {
				t.Fatalf("%s: the continuation never completes", scope)
			}
			found, err := db.FindMessages(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			for _, h := range found.Messages {
				got = append(got, h.Content[strings.LastIndex(h.Content, " ")+1:])
			}
			if found.Complete {
				break
			}
			req.Before = found.SearchedTo
		}
		if strings.Join(got, " ") != "6 5 4 3 2 1 0" {
			t.Errorf("%s: the calls returned %v, want 6 5 4 3 2 1 0", scope, got)
		}
	}
}

func TestFindMessagesRefusesABoundThatIsNotAnInstant(t *testing.T) {
	db := memDB(t)
	for _, req := range []apiv1.FindRequest{
		{Query: "needle", Scope: "tg:1", Since: "yesterday"},
		{Query: "needle", Scope: "tg:1", Before: "2026-11-25"},
	} {
		if found, err := db.FindMessages(t.Context(), req); err == nil {
			t.Errorf("%+v answered %+v, want a refusal", req, found)
		}
	}
}

// GET /v1/messages/find answers the contract find_messages answers, and
// confines a folder token before it searches: `limit` counts what the caller
// can read, an empty scope is the token's folder, and a foreign scope is 403.
func TestRESTFindMessagesConfinesTheScopeBeforeTheSearch(t *testing.T) {
	read := []string{"chats:read:own_group"}
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: read, folder: "alice"})
	now := time.Now().Add(-time.Hour)
	for i := range 6 {
		folder, jid := "bob", "web:bob"
		if i < 2 {
			folder, jid = "alice", "web:alice"
		}
		if err := db.PutMessage(core.Message{
			ID: fmt.Sprint("m", i), ChatJID: jid, Sender: "u", RoutedTo: folder,
			Content: fmt.Sprint("needle ", i), Timestamp: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(query url.Values) (int, map[string]any) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/v1/messages/find?"+query.Encode(), nil))
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("body %q: %v", rec.Body.String(), err)
		}
		return rec.Code, body
	}

	code, body := get(url.Values{"query": {"needle"}, "limit": {"2"}})
	if code != 200 || body["count"] != float64(2) {
		t.Fatalf("alice's own search = %d %v; bob's 4 newer hits must not use up limit", code, body)
	}
	for _, key := range []string{"messages", "count", "searched_to", "complete"} {
		if _, ok := body[key]; !ok {
			t.Errorf("the answer carries no %q: %v", key, body)
		}
	}
	hit := body["messages"].([]any)[0].(map[string]any)
	if hit["chat_jid"] != "web:alice" || hit["content"] != "«needle» 1" {
		t.Errorf("first hit = %v, want alice's newest", hit)
	}
	if _, ranked := hit["rank"]; ranked {
		t.Errorf("a hit carries a rank: %v", hit)
	}

	code, body = get(url.Values{"query": {"needle"}, "limit": {"1"}})
	if code != 200 || body["complete"] != false {
		t.Fatalf("limit 1 = %d %v, want an incomplete answer", code, body)
	}
	code, body = get(url.Values{"query": {"needle"}, "limit": {"1"},
		"before": {body["searched_to"].(string)}})
	if code != 200 || body["count"] != float64(1) ||
		body["messages"].([]any)[0].(map[string]any)["content"] != "«needle» 0" {
		t.Fatalf("the continuation = %d %v, want alice's older hit", code, body)
	}

	for _, scope := range []string{"bob", "web:bob"} {
		if code, _ := get(url.Values{"query": {"needle"}, "scope": {scope}}); code != 403 {
			t.Errorf("scope %s = %d, want 403", scope, code)
		}
	}
}
