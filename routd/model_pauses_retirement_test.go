package routd

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

type retirementSnapshot struct {
	objstore.Store
	fired bool
	after func()
}

func (s *retirementSnapshot) Query(ctx context.Context, q objstore.Query) ([]objstore.Item, error) {
	items, err := s.Store.Query(ctx, q)
	if err == nil && !s.fired && q.Entity == store.TurnContexts && q.Key.Op == objstore.AnyKey {
		s.fired = true
		s.after()
	}
	return items, err
}

// A turn rebound to a surviving group keeps pending input through deletion.
func TestDeletionPreservesSurvivingReboundInput(t *testing.T) {
	db, l, p, _ := pauseFixture(t)
	jid := "telegram:group/rebound"
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "a"}})
	pauseInput(t, db, jid, "input", "sender", "surviving private input", time.Now())
	p.busy = true
	processPauseChat(t, l, jid)
	before, ok := db.GetTurnContext("input")
	if !ok || before.Folder != "a" || before.State != store.TurnRunning || db.GetAgentCursor(jid) != "" {
		t.Fatalf("initial busy attempt: %+v cursor=%q", before, db.GetAgentCursor(jid))
	}
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "b"}})
	gate := &retirementSnapshot{Store: db.Obj(), after: func() {
		l.q.busyWait.Delete(jid)
		processPauseChat(t, l, jid)
		tc, _ := db.GetTurnContext("input")
		if tc.Folder != "b" || tc.State != store.TurnRunning || tc.AttemptID == before.AttemptID {
			t.Fatalf("surviving rebind: %+v", tc)
		}
	}}
	if _, _, err := store.NewOver(gate).DeleteGroup("a"); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetTurnContext("input")
	if !gate.fired || !db.GroupExists("b") || after.Folder != "b" || after.State != store.TurnRunning {
		t.Fatalf("surviving group/turn changed: %+v", after)
	}
	if got := db.GetAgentCursor(jid); got != "" {
		t.Fatalf("surviving input cursor = %q, want empty", got)
	}
	p.busy = false
	l.q.busyWait.Delete(jid)
	n := len(p.runs)
	processPauseChat(t, l, jid)
	if len(p.runs) != n+1 || string(p.runs[n].Folder) != "b" || !strings.Contains(p.runs[n].MessageBatch, "surviving private input") {
		t.Fatalf("surviving input not dispatched to b: %+v", p.runs[n:])
	}
}
