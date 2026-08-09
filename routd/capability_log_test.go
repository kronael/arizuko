package routd

// BUGS F63: a denied container capability left no signal anywhere. web:publish
// resolving false makes container/runner skip both web bind mounts, so the
// agent simply finds no ~/public_html — indistinguishable from a broken mount,
// and diagnosed as one (wrongly, four times) because the host had nothing to
// grep. dispatchRun now logs every capability decision at the point it makes
// them; these tests assert the line exists AND tracks the real decision.

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// captureInfoLog redirects the default slog to a buffer for the test's duration.
func captureInfoLog(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// dispatchOnce drives one turn through the loop and returns the RunRequest
// runed would have received.
func dispatchOnce(t *testing.T, db *DB) runedv1.RunRequest {
	t.Helper()
	var got runedv1.RunRequest
	runner := runnerFn(func(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
		got = req
		_, _ = db.RecordTurnResult(string(req.Folder), req.TurnID, "s1", "success")
		return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
	})
	loop := NewLoop(db, runner, LoopConfig{})
	loop.StopQueue()
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	_ = db.PutMessage(core.Message{ID: "m1", ChatJID: "slack:T/C/U", Sender: "u1",
		Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})
	if _, err := loop.processGroupMessages("slack:T/C/U"); err != nil {
		t.Fatalf("process: %v", err)
	}
	return got
}

// TestDispatchLogsDeniedWebPublish is F63 itself: the folder holds no
// web:publish grant, the web surface is silently absent, and the ONLY
// attribution an operator can get is this line.
func TestDispatchLogsDeniedWebPublish(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})

	logged := captureInfoLog(t)
	req := dispatchOnce(t, db)

	if req.WebPublish {
		t.Fatal("ungranted folder got WebPublish (test premise broken)")
	}
	out := logged()
	if !strings.Contains(out, "web_publish=false") {
		t.Errorf("denial not logged; an absent ~/public_html stays unattributable.\nlog: %s", out)
	}
	// The folder must be named, or the line cannot be traced to a grant.
	if !strings.Contains(out, "folder=demo") {
		t.Errorf("capability log does not name the folder.\nlog: %s", out)
	}
	// The other two decisions ride the same line (BUGS F63 asks for all three).
	for _, want := range []string{"egress=false", "share_readonly="} {
		if !strings.Contains(out, want) {
			t.Errorf("capability log missing %q.\nlog: %s", want, out)
		}
	}
}

// TestDispatchLogsGrantedWebPublish pins the line to the DECISION rather than a
// constant: granting web:publish must flip both the RunRequest and the log.
func TestDispatchLogsGrantedWebPublish(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_ = db.PutGroup(core.Group{Folder: "demo"})

	tx, err := db.SQL().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := grantACLTx(t.Context(), tx, "folder:demo", "demo", "web:publish", "", "test"); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	logged := captureInfoLog(t)
	req := dispatchOnce(t, db)

	if !req.WebPublish {
		t.Fatal("granted folder did not get WebPublish (test premise broken)")
	}
	if out := logged(); !strings.Contains(out, "web_publish=true") {
		t.Errorf("capability log did not follow the grant.\nlog: %s", out)
	}
}
