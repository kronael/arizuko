package routd

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type folderMessageReads struct {
	objstore.Store
	queries []objstore.Query
	rows    int
	err     error
}

func (s *folderMessageReads) Query(ctx context.Context,
	q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Messages && s.err != nil {
		return nil, s.err
	}
	items, err := s.Store.Query(ctx, q)
	if q.Entity == store.Messages {
		s.queries = append(s.queries, q)
		s.rows += len(items)
	}
	return items, err
}

// Queue admission reads the pending batch once, bounded by pollBatch, and
// decides it as the worker does: the bot row "newest" never resolves a batch, so
// slack:T/C keys on "old", the folder its user rows dispatch to.
func TestFolderForJidReadsThePendingBatchOnce(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, folder := range []string{"old", "new", "direct"} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	doSetRoutes(t, db, []core.Route{
		{Seq: 0, Match: "sender=newest", Target: "new"},
		{Seq: 1, Match: "platform=slack", Target: "old"},
	})
	now := time.Now().UTC()
	for _, msg := range []core.Message{
		{ID: "old", ChatJID: "slack:T/C", Sender: "old",
			Timestamp: now.Add(-time.Hour)},
		{ID: "future", ChatJID: "slack:T/C", Sender: "old",
			Timestamp: now.Add(time.Hour)},
		{ID: "newest", ChatJID: "slack:T/C", Sender: "newest",
			Timestamp: now.Add(2 * time.Hour), BotMsg: true},
		{ID: "direct", ChatJID: "web:direct", Timestamp: now},
		{ID: "miss", ChatJID: "unknown:chat", Timestamp: now},
	} {
		if err := db.PutMessage(msg); err != nil {
			t.Fatal(err)
		}
	}
	reads := &folderMessageReads{Store: db.obj}
	db.obj = reads
	loop := &Loop{db: db}
	for _, tt := range []struct {
		jid  string
		want string
		rows int
	}{
		{"slack:T/C", "old", 3},
		{"web:direct", "direct", 1},
		{"unknown:chat", "unknown:chat", 1},
		{"slack:empty", "slack:empty", 0},
		{"web:empty", "web:empty", 0},
	} {
		t.Run(tt.jid, func(t *testing.T) {
			reads.queries, reads.rows = nil, 0
			if got := loop.folderForJid(tt.jid); got != tt.want {
				t.Fatalf("folder = %q, want %q", got, tt.want)
			}
			if len(reads.queries) != 1 || reads.rows != tt.rows {
				t.Fatalf("read %d queries, %d rows; want one query, %d rows",
					len(reads.queries), reads.rows, tt.rows)
			}
			q := reads.queries[0]
			if q.Partition != tt.jid || q.Limit != pollBatch {
				t.Fatalf("read is not the worker's bounded batch read: %+v", q)
			}
		})
	}
	reads.err = errors.New("message read failed")
	if got := loop.folderForJid("slack:T/C"); got != "slack:T/C" {
		t.Fatalf("folder after read failure = %q, want chat JID", got)
	}
}

type storeQueries struct {
	objstore.Store
	n int
}

func (s *storeQueries) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	s.n++
	return s.Store.Query(ctx, q)
}

// Resolving a batch asks the store for the chat's engagement and sticky pins
// once per distinct fact: 60 rows on one topic cost what 2 rows cost.
func TestBatchResolve_ReadsEachChatFactOnce(t *testing.T) {
	db, loop := newEngagementLoop(t, engagedCIRoutes(t))
	jid := "github:kronael/arizuko/pr/9"
	if err := db.SetEngagement(jid, "", "held", time.Hour); err != nil {
		t.Fatal(err)
	}
	reads := &storeQueries{Store: db.obj}
	db.obj = reads
	batch := func(n int) []core.Message {
		msgs := make([]core.Message, n)
		for i := range msgs {
			verb := "message"
			if i%2 == 1 {
				verb = "check_run"
			}
			msgs[i] = core.Message{ChatJID: jid, Topic: "pr/9", Verb: verb, Sender: "u"}
		}
		return msgs
	}
	loop.resolveBatch(jid, batch(2))
	two := reads.n
	reads.n = 0
	b := loop.resolveBatch(jid, batch(60))
	if reads.n != two {
		t.Fatalf("60 rows read the store %d times, 2 rows %d: reads grow with the batch", reads.n, two)
	}
	if !b.ok || b.Folder != "held" {
		t.Fatalf("batch resolution changed: %+v", b.resolution)
	}
}
