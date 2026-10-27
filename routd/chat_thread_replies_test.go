package routd

import (
	"context"
	"errors"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

func TestChatThreadRepliesAreIndependentWithinFolder(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	dl := &recDeliverer{canThread: true}
	srv := NewServer(db, nil, dl, nil, 0, "")
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	if err := db.store().WriteEntities(context.Background(), nil, objstore.Write{Op: objstore.Merge, Entity: store.Groups, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: "demo"}, Attrs: map[string]string{"config": `{"thread_replies":0}`}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		jid, turn string
		on        bool
	}{
		{"discord:g/a", "one", false}, {"discord:g/b", "two", true},
	} {
		if err := db.SetChatIsGroup(tc.jid, true); err != nil {
			t.Fatal(err)
		}
		var ws store.WriteSet
		if err := db.store().SetChatThreadRepliesInto(&ws, tc.jid, &tc.on); err != nil {
			t.Fatal(err)
		}
		if err := db.store().Apply(context.Background(), &ws); err != nil {
			t.Fatal(err)
		}
		threadTurn(t, db, tc.turn, "demo", tc.jid, "trigger")
		turn, _ := db.GetTurnContext(tc.turn)
		place, err := placeTurnDelivery(db, dl, turn, tc.jid, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if (place.threadRoot != "") != tc.on {
			t.Fatalf("%s placement = %+v", tc.jid, place)
		}
		if err := srv.declareSummary(turn, tc.jid, "task"); err != nil {
			t.Fatal(err)
		}
		_, found := db.MessageByID(tc.jid, summaryID(tc.turn))
		if found != tc.on {
			t.Fatalf("%s summary opener = %v", tc.jid, found)
		}
	}
}

type chatQueryFailure struct{ objstore.Store }

func (s chatQueryFailure) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if q.Entity == store.Chats {
		return nil, errors.New("chat read failed")
	}
	return s.Store.Query(ctx, q)
}

func TestChatThreadRepliesReadFailureAndExistingTopic(t *testing.T) {
	db, srv := newSummaryTurn(t, &recDeliverer{canThread: true})
	tc, _ := db.GetTurnContext("t1")
	if err := db.store().WriteEntities(context.Background(), nil, objstore.Write{Op: objstore.Merge, Entity: store.Chats, Key: objstore.Key{Partition: store.OwnerRoutd, Sort: progressJID}, Attrs: map[string]string{"thread_replies": "invalid"}}); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []objstore.Store{db.obj, chatQueryFailure{db.obj}} {
		db.obj = broken
		if _, err := placeTurnDelivery(db, srv.deliver, tc, progressJID, "", true); err == nil {
			t.Fatal("unreadable preference accepted")
		}
		if err := srv.declareSummary(tc, progressJID, "task"); err == nil {
			t.Fatal("summary accepted unreadable preference")
		}
		if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Kind: ipc.StatusProgress, Text: "progress"}); err == nil {
			t.Fatal("progress accepted unreadable preference")
		}
		existing := tc
		existing.Topic = "existing"
		place, err := placeTurnDelivery(db, srv.deliver, existing, progressJID, "", true)
		if err != nil || place.threadID != "existing" {
			t.Fatalf("existing topic = %+v, %v", place, err)
		}
	}
}
