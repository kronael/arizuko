package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

// erroredDB opens a routd DB, routes each chat to its folder and flags it
// errored — the state an operator's inspection reads.
func erroredDB(t *testing.T, routes map[string]string) *DB {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	seq := 1
	for jid, folder := range routes {
		if _, err := db.AddRoute(core.Route{
			Seq: seq, Match: "chat_jid=" + jid, Target: folder,
		}); err != nil {
			t.Fatal(err)
		}
		seq++
		if err := db.MarkChatErrored(jid); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

// An operator scoped to a folder sees the errored chats that folder owns and no
// others; the root sees every one.
func TestErroredChatsScopedByFolder(t *testing.T) {
	db := erroredDB(t, map[string]string{"tg:1": "world/a", "tg:2": "world/b"})
	now := time.Now()
	for _, m := range []core.Message{
		{ID: "a1", ChatJID: "tg:1", Sender: "u", Content: "x", Timestamp: now, RoutedTo: "world/a"},
		{ID: "a2", ChatJID: "tg:1", Sender: "u", Content: "y",
			Timestamp: now.Add(time.Second), RoutedTo: "world/a"},
		{ID: "b1", ChatJID: "tg:2", Sender: "u", Content: "z", Timestamp: now, RoutedTo: "world/b"},
	} {
		if err := db.PutMessage(m); err != nil {
			t.Fatal(err)
		}
	}

	all, err := db.ErroredChats("", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Errorf("root: want 2 chats, got %d", len(all))
	}

	scoped, err := db.ErroredChats("world/a", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(scoped) != 1 || scoped[0].ChatJID != "tg:1" {
		t.Fatalf("scoped world/a: want [tg:1], got %+v", scoped)
	}
	if scoped[0].Count != 2 {
		t.Errorf("want count=2, got %d", scoped[0].Count)
	}

	b, err := db.ErroredChats("world/b", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != 1 || b[0].ChatJID != "tg:2" {
		t.Errorf("scoped world/b: want [tg:2], got %+v", b)
	}
}

// `_` is a legal folder character and a LIKE wildcard, so a containment test
// written as a pattern hands one folder's errored chats to a neighbour's
// operator (BUGS L122).
func TestErroredChatsAreNotWidenedByLikeMetacharacters(t *testing.T) {
	db := erroredDB(t, map[string]string{"tg:a_b-one": "a_b/one", "tg:aXb-two": "aXb/two"})
	got, err := db.ErroredChats("a_b", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ChatJID != "tg:a_b-one" {
		t.Errorf("ErroredChats(a_b) = %+v, want only its own chat", got)
	}
}
