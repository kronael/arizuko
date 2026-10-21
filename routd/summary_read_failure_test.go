package routd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type summaryReadFailure struct {
	objstore.Store
	err error
}

func (s *summaryReadFailure) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	if s.err != nil && q.Entity == store.Messages && q.Partition == progressJID &&
		q.Limit == 0 && len(q.Where) == 0 {
		return nil, s.err
	}
	return s.Store.Query(ctx, q)
}

func TestTaskSummaryReadFailureCannotMoveTheAnswer(t *testing.T) {
	dl := &recDeliverer{}
	db, srv, sock := summaryBroker(t, dl)
	declareTask(t, srv)
	if err := db.SetTurnTriggerMsg("t1", "human-trigger"); err != nil {
		t.Fatal(err)
	}
	before := len(dl.sends)
	reads := &summaryReadFailure{Store: db.obj, err: errors.New("summary query unavailable")}
	db.obj = reads
	_, message := callToolOverSock(t, sock, "reply", map[string]any{
		"chatJid": progressJID, "text": "the result",
	})
	if len(dl.sends) != before {
		t.Fatalf("summary read failure delivered an answer elsewhere: last=%+v tool=%q", dl.sends[len(dl.sends)-1], message)
	}
	if !strings.Contains(message, "not delivered, and no retry follows") ||
		!strings.Contains(message, "retry after storage recovers") {
		t.Fatalf("unplaced answer must not be queued: %q", message)
	}
	for _, status := range []ipc.TurnStatus{
		{TurnID: "t1", Kind: ipc.StatusSummary, Text: "a duplicate declaration"},
		{TurnID: "t1", Kind: ipc.StatusProgress, Text: "☐ verify"},
	} {
		if err := srv.submitStatus(status); err == nil || !strings.Contains(err.Error(), "summary query unavailable") {
			t.Fatalf("%s hid the query failure: %v", status.Kind, err)
		}
	}
	if _, err := srv.mcpCheckTurn("t1"); err == nil || !strings.Contains(err.Error(), "summary query unavailable") {
		t.Fatalf("completion check hid the query failure: %v", err)
	}
	if len(dl.sends) != before || len(dl.edits) != 0 {
		t.Fatal("failed declaration or progress changed the visible messages")
	}
	reads.err = nil
	rows, err := db.PendingOutbound(time.Now().Add(time.Hour), 10)
	if err != nil || len(rows) != 0 {
		t.Fatalf("unplaced child retained for replay: rows=%+v err=%v", rows, err)
	}
	NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl}).maybeRetryOutbound(time.Now().Add(time.Hour))
	if len(dl.sends) != before {
		t.Fatal("sweep delivered a refused child")
	}
	if _, message := callToolOverSock(t, sock, "reply", map[string]any{
		"chatJid": progressJID, "text": "the recovered result",
	}); message != "" {
		t.Fatal(message)
	}
	opener, ok := db.MessageByID(progressJID, summaryID("t1"))
	if !ok || len(dl.sends) != before+1 || dl.sends[before].threadRoot != opener.PlatformID {
		t.Fatalf("recovered reply left the opener: sends=%+v opener=%+v", dl.sends, opener)
	}
}
