package routd

import (
	"context"
	"testing"

	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// captureRunner records the RunRequest routd builds, so a test can assert what
// the spawn is actually told rather than what the call site looks like.
type captureRunner struct{ req runedv1.RunRequest }

func (c *captureRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	c.req = req
	return runedv1.RunOutcome{Outcome: runedv1.OutcomeSilent}, nil
}

func newCaptureLoop(t *testing.T) (*DB, *Loop, *captureRunner) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatalf("open mem db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	r := &captureRunner{}
	loop := NewLoop(db, r, LoopConfig{GroupsDir: t.TempDir()})
	loop.StopQueue()
	return db, loop, r
}

// ROUTING.md § Topics isolate threads inside one group: context_mode "isolated" means no
// `--resume`. routd sent the group's session id anyway, so every isolated cron
// resumed the chat transcript and appended its own tool-heavy run to it — the
// folder's crons writing themselves into the humans' conversation until Claude
// Code compacted the humans out (BUGS L54, sloth #trading: 8 cron turns to 4
// human ones in one transcript).
func TestDispatchRun_IsolatedTaskGetsNoSession(t *testing.T) {
	db, loop, runner := newCaptureLoop(t)
	folder := "main/trading"
	if err := db.PutSession(folder, "", "a2fb6376-30aa-498c-be8c-c13d58312ab1"); err != nil {
		t.Fatalf("put session: %v", err)
	}

	if _, err := loop.dispatchRun(context.Background(), folder, "", "discord:g/c",
		"turn-1", "timed-isolated:task-1", "run the monitor", false, ""); err != nil {
		t.Fatalf("dispatchRun: %v", err)
	}

	if !runner.req.Isolated {
		t.Error("Isolated = false for a timed-isolated: trigger")
	}
	if runner.req.SessionID != "" {
		t.Errorf("SessionID = %q; an isolated task must not resume the chat transcript",
			runner.req.SessionID)
	}
}

// The mirror: a "group" task (sender "timed") and an ordinary chat turn DO
// resume the folder's session — that is the whole point of the default mode.
func TestDispatchRun_GroupTaskResumesSession(t *testing.T) {
	db, loop, runner := newCaptureLoop(t)
	folder := "main/trading"
	const sid = "a2fb6376-30aa-498c-be8c-c13d58312ab1"
	if err := db.PutSession(folder, "", sid); err != nil {
		t.Fatalf("put session: %v", err)
	}

	if _, err := loop.dispatchRun(context.Background(), folder, "", "discord:g/c",
		"turn-1", "timed", "run the monitor", false, ""); err != nil {
		t.Fatalf("dispatchRun: %v", err)
	}

	if runner.req.Isolated {
		t.Error("Isolated = true for a plain timed trigger")
	}
	if runner.req.SessionID != sid {
		t.Errorf("SessionID = %q, want %q", runner.req.SessionID, sid)
	}
}
