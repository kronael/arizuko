package routd

import (
	"context"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// routd hands ant the fact that a progress line can show, once, at dispatch: a
// person is owed an answer and the reply chat edits and deletes. ant nudges for
// a status only then, so a cron turn, a sink without delete and a sink without
// edit get no nudge to answer.
func TestDispatch_ProgressFlagNeedsAnAddressedTurnAndAShowingSink(t *testing.T) {
	for _, tc := range []struct {
		name, sender         string
		noEdit, noDelete, on bool
	}{
		{"addressed on a sink that edits and deletes", "human", false, false, true},
		{"scheduled task", "timed", false, false, false},
		{"sink without delete", "human", false, true, false},
		{"sink without edit", "human", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dl := &recDeliverer{noEdit: tc.noEdit, noDelete: tc.noDelete}
			db, srv, _ := newTestRoutd(t)
			srv.deliver = dl
			srv.loop.deliver = dl
			if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
			var got *runedv1.RunRequest
			srv.loop.runner = runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
				got = &req
				return runedv1.RunOutcome{RunID: "run", Outcome: runedv1.OutcomeOK}, nil
			})
			if err := db.PutMessage(core.Message{
				ID: "t1", ChatJID: progressJID, Sender: tc.sender, Verb: "mention",
				Content: "request", Timestamp: time.Now(),
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := srv.loop.processGroupMessages(progressJID); err != nil {
				t.Fatal(err)
			}
			if got == nil || got.Progress != tc.on {
				t.Fatalf("run request = %+v, want Progress=%v", got, tc.on)
			}
		})
	}
}
