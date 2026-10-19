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

func TestFolderForJidReadsOnlyNewestMessage(t *testing.T) {
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
		{"slack:T/C", "new", 1},
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
			if q.Partition != tt.jid || !q.Descending || q.Limit != 1 {
				t.Fatalf("unbounded newest-message read: %+v", q)
			}
		})
	}
	reads.err = errors.New("message read failed")
	if got := loop.folderForJid("slack:T/C"); got != "slack:T/C" {
		t.Fatalf("folder after read failure = %q, want chat JID", got)
	}
}
