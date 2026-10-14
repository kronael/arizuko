package routd

import (
	"context"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

type approvalBusyRunner struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	wait    func() bool
	verdict chan bool
	batches chan string
}

func (r *approvalBusyRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	r.batches <- req.MessageBatch
	r.once.Do(func() {
		close(r.started)
		r.verdict <- r.wait()
	})
	<-r.release
	return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
}

func TestApprovalIngressWhileChatIsBusy(t *testing.T) {
	for _, platform := range []string{"telegram", "slack", "discord"} {
		for _, command := range []string{"/approve", "/reject", "/always"} {
			t.Run(platform+command, func(t *testing.T) {
				waitForVerdict(t)
				db := memDB(t)
				jid := map[string]string{
					"telegram": "telegram:user/1112184352",
					"slack":    "slack:T012/channel/C1",
					"discord":  "discord:g1/ch-1",
				}[platform]
				sender := platform + ":user/operator"
				if err := db.PutGroup(core.Group{Folder: "atlas"}); err != nil {
					t.Fatal(err)
				}
				doSetRoutes(t, db, []core.Route{{Match: "", Target: "atlas"}})
				if err := db.PutPendingAction(PendingAction{ID: "held-busy", GroupFolder: "atlas", CallerAgent: "folder:atlas", Tool: "like", Args: "{}", ChatJID: jid}); err != nil {
					t.Fatal(err)
				}
				addACL(t, db, sender, "admin", "atlas", "allow")
				srv := NewServer(db, nil, nil, nil, 0, "")
				runner := &approvalBusyRunner{started: make(chan struct{}), release: make(chan struct{}), verdict: make(chan bool, 1), batches: make(chan string, 10), wait: func() bool { _, held := srv.awaitVerdict("held-busy", "like"); return held }}
				loop := NewLoop(db, runner, LoopConfig{})
				srv.loop = loop
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(runner.release) }) }
				t.Cleanup(func() {
					loop.StopQueue()
					if row, _ := pendingActionRow(db, "held-busy"); row.Status == PendingHeld {
						if _, err := resolvePending(db, "held-busy", PendingRejected, sender, ""); err != nil {
							t.Error(err)
						}
					}
					release()
					awaitCond(t, "queue did not release busy turn", func() bool { return loop.q.ActiveCount() == 0 })
				})
				ingress := http.HandlerFunc(srv.handleMessages)
				post := func(id, actor, text string) {
					t.Helper()
					m := apiv1.Message{ID: id, ChatJID: jid, Sender: actor, Content: text, Verb: "mention"}
					if rec := doJSON(t, ingress, "POST", "/v1/messages", "", m); rec.Code != 200 {
						t.Fatal(rec.Body.String())
					}
				}
				post("task", sender, "do the task")
				select {
				case <-runner.started:
				case <-time.After(5 * time.Second):
					t.Fatal("turn did not start")
				}
				<-runner.batches
				cursor := db.GetAgentCursor(jid)
				post("before", sender, "ordinary message before")
				post("refused", platform+":user/bystander", command+" held-busy")
				if row, _ := pendingActionRow(db, "held-busy"); row.Status != PendingHeld {
					t.Fatal("bystander decided the held call")
				}
				post("press", sender, command+" held-busy")
				post("after", sender, "ordinary message after")
				want := PendingApproved
				if command == "/reject" {
					want = PendingRejected
				}
				row, ok := pendingActionRow(db, "held-busy")
				if !ok || row.Status != want || row.ReviewedBy != sender {
					t.Fatalf("button during active turn: status=%q reviewer=%q, want %s by %s", row.Status, row.ReviewedBy, want, sender)
				}
				select {
				case held := <-runner.verdict:
					if held != (command == "/reject") {
						t.Fatalf("original waiter returned held=%v", held)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("original waiter did not receive verdict")
				}
				post("press", sender, command+" held-busy")
				post("repeat", sender, command+" held-busy")
				if again, _ := pendingActionRow(db, "held-busy"); !reflect.DeepEqual(again, row) {
					t.Fatal("duplicate press changed canonical decision")
				}
				if db.GetAgentCursor(jid) != cursor {
					t.Fatal("verdict advanced past unread ordinary messages")
				}
				for _, id := range []string{"before", "after"} {
					if _, ok := db.MessageByID(jid, id); !ok {
						t.Fatalf("ordinary message %s lost", id)
					}
				}
				for _, id := range []string{"refused", "press", "repeat"} {
					if _, ok := db.MessageByID(jid, id); ok {
						t.Fatalf("consumed verdict %s enters turn feed", id)
					}
				}
				acls, err := db.ListACL("")
				if err != nil {
					t.Fatal(err)
				}
				var skip int
				for _, acl := range acls {
					if acl.Principal == "folder:atlas" && acl.Action == auth.HoldPrefix+"like" && acl.Scope == "atlas" && acl.Effect == auth.EffectSkipApproval {
						skip++
					}
				}
				expected := 0
				if command == "/always" {
					expected = 1
				}
				if skip != expected {
					t.Fatalf("standing rows=%d, want %d", skip, expected)
				}
				release()
				awaitCond(t, "original turn did not return", func() bool { return loop.q.ActiveCount() == 0 })
				loop.pollOnce()
				select {
				case batch := <-runner.batches:
					if !strings.Contains(batch, "ordinary message before") || !strings.Contains(batch, "ordinary message after") || strings.Contains(batch, command) {
						t.Fatalf("ordinary follow-up batch lost messages or included verdict: %s", batch)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("ordinary messages did not reach next turn")
				}
			})
		}
	}
}
