package routd

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/ipc"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

type budgetRunner struct {
	t       *testing.T
	srv     *Server
	mu      sync.Mutex
	folders []string
}

func (r *budgetRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.mu.Lock()
	r.folders = append(r.folders, string(req.Folder))
	r.mu.Unlock()
	result := ipc.TurnResult{TurnID: req.TurnID, Status: "success"}
	if req.Folder == "a" {
		result.Status, result.Error = "budget", "429 budget_exceeded: team a spent its cap"
	} else {
		r.srv.replyForTest(r.t, req.TurnID, req.ChatJID, "answered")
	}
	if _, err := r.srv.mcpSubmitTurn(string(req.Folder), result); err != nil {
		return runedv1.RunOutcome{}, err
	}
	return runedv1.RunOutcome{RunID: "run-" + req.TurnID, Outcome: runedv1.OutcomeOK}, nil
}

func (r *budgetRunner) ran() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.folders...)
}

// A turn the gateway refused for a spent cap ends like a model refusal: one
// notice, a stored cause, no replay, and the run that returns OK afterwards
// leaves the instance breaker closed, so another folder's turn still runs
// (BUGS L901).
func TestTurnBudgetEndsTheTurnWithoutOpeningTheBreaker(t *testing.T) {
	logs := captureWarnings(t)
	db, l, p, dl := pauseFixture(t)
	runner := &budgetRunner{t: t, srv: p.srv}
	l.runner = runner
	pauseInput(t, db, "web:a", "spent", "u", "question", time.Now())
	processPauseChat(t, l, "web:a")
	processPauseChat(t, l, "web:a")

	want := "Budget reached for today. Automatic retries stopped."
	notices := 0
	for _, m := range dl.sentMsgs() {
		if strings.Contains(m.text, "Budget reached for today") {
			notices++
		}
		if strings.Contains(m.text, "budget_exceeded") {
			t.Fatalf("raw gateway error reached the chat: %q", m.text)
		}
	}
	if notices != 1 {
		t.Fatalf("budget notices = %d, want 1: %+v", notices, dl.sentMsgs())
	}
	if got := db.store().TurnFailure("a", "spent"); got != want {
		t.Fatalf("stored cause %q, want %q", got, want)
	}
	if !strings.Contains(logs.String(), "level=WARN") || !strings.Contains(logs.String(), "budget") {
		t.Fatalf("no budget WARN; log:\n%s", logs.String())
	}
	if got := runner.ran(); len(got) != 1 {
		t.Fatalf("input replayed: runs in folders %v, want one", got)
	}
	tc, _ := db.GetTurnContext("spent")
	control, exists, err := db.store().ReadTurnControl(t.Context())
	if err != nil || (exists && control.Phase != "closed") {
		t.Fatalf("control %+v exists=%v err=%v state=%s", control, exists, err, tc.State)
	}

	pauseInput(t, db, "web:b", "other", "u", "question", time.Now())
	processPauseChat(t, l, "web:b")
	if got := runner.ran(); len(got) != 2 || got[1] != "b" {
		t.Fatalf("another folder's turn was not admitted: runs in folders %v", got)
	}
}
