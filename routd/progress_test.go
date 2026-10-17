package routd

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

const progressJID = "slack:T/C/U"

func newProgressTurn(t *testing.T, dl *recDeliverer, returnTo string) (*DB, *Server) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, dl, nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", progressJID, "u1", returnTo); err != nil {
		t.Fatal(err)
	}
	return db, srv
}

func statusRows(t *testing.T, db *DB, jid string) []core.Message {
	t.Helper()
	msgs, err := db.MessagesSince(jid, "")
	if err != nil {
		t.Fatal(err)
	}
	var rows []core.Message
	for _, m := range msgs {
		if m.Verb == verbStatus {
			rows = append(rows, m)
		}
	}
	return rows
}

func TestProgress_EqualTextIsNoOp(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")

	for range 2 {
		if err := srv.mcpSubmitStatus("t1", "verified; writing review"); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 1 || len(dl.edits) != 0 {
		t.Fatalf("equal text: sends=%d edits=%d, want 1 and 0: %+v %+v",
			len(dl.sends), len(dl.edits), dl.sends, dl.edits)
	}
	if rows := statusRows(t, db, progressJID); len(rows) != 1 {
		t.Fatalf("status rows=%d, want 1", len(rows))
	}
}

func TestProgress_NoEditNoMessage(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dl := &recDeliverer{noEdit: true}
	db, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "a"); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 0 || len(dl.edits) != 0 {
		t.Fatalf("a channel without edit received progress: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
	if n := countBots(t, db, progressJID); n != 0 {
		t.Fatalf("rows=%d, want 0", n)
	}
	if got := strings.Count(logs.String(), "progress update dropped"); got != 1 {
		t.Fatalf("debug lines=%d, want 1: %s", got, logs.String())
	}
	if !strings.Contains(logs.String(), "turn_id=t1") {
		t.Fatalf("debug line does not name the turn: %s", logs.String())
	}
}

func TestProgress_SecondUpdateEditsAndRewritesRow(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")

	for _, s := range []string{"first", "second"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	rows := statusRows(t, db, progressJID)
	if len(rows) != 1 || rows[0].Content != "⏳ second" {
		t.Fatalf("row after edit = %+v, want one row reading %q", rows, "⏳ second")
	}
	if len(dl.edits) != 1 || dl.edits[0].content != "⏳ second" {
		t.Fatalf("edits=%+v", dl.edits)
	}
}

func TestProgress_HardEditErrorSurfaces(t *testing.T) {
	dl := &recDeliverer{editErr: errSend}
	db, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "a"); err != nil {
		t.Fatal(err)
	}
	if err := srv.mcpSubmitStatus("t1", "b"); !errors.Is(err, errSend) {
		t.Fatalf("hard edit error = %v, want errSend", err)
	}
	if len(dl.sends) != 1 {
		t.Fatalf("hard edit error re-posted: sends=%+v", dl.sends)
	}
	if rows := statusRows(t, db, progressJID); rows[0].Content != "⏳ a" {
		t.Fatalf("row rewritten although the platform edit failed: %q", rows[0].Content)
	}
}

func TestProgress_RefusedEditPostsNothing(t *testing.T) {
	dl := &recDeliverer{editErr: chanlib.ErrUnsupported}
	db, srv := newProgressTurn(t, dl, "")

	for _, s := range []string{"a", "b"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 1 {
		t.Fatalf("ErrUnsupported fell back to a fresh send: %+v", dl.sends)
	}
	if rows := statusRows(t, db, progressJID); len(rows) != 1 || rows[0].Content != "⏳ a" {
		t.Fatalf("rows=%+v, want the single row unchanged", rows)
	}
}

func TestProgress_ResultStatusAlreadyStreamedPostsOnce(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "verified; writing review"); err != nil {
		t.Fatal(err)
	}
	_, err := srv.recordTurnResult("t1", apiv1.TurnResult{
		TurnID: "t1", SessionID: "sX", Status: "success",
		Result: "<status>verified; writing review</status>the review",
	})
	if err != nil {
		t.Fatal(err)
	}
	var marks int
	for _, m := range dl.sends {
		if strings.HasPrefix(m.text, progressMark) {
			marks++
		}
	}
	if marks != 1 || len(dl.edits) != 1 || dl.edits[0].content != "✓ verified; writing review" {
		t.Fatalf("progress posts=%d edits=%+v, want 1 post and only the terminal edit: %+v", marks, dl.edits, dl.sends)
	}
	if len(dl.sends) != 2 || dl.sends[1].text != "the review" {
		t.Fatalf("the reply was lost: %+v", dl.sends)
	}
	if rows := statusRows(t, db, progressJID); len(rows) != 1 {
		t.Fatalf("status rows=%d, want 1", len(rows))
	}
}

func TestProgress_FailedResultUpdateStillDeliversReply(t *testing.T) {
	dl := &recDeliverer{editErr: errSend}
	_, srv := newProgressTurn(t, dl, "")

	if err := srv.mcpSubmitStatus("t1", "a"); err != nil {
		t.Fatal(err)
	}
	srv.appendAndDeliver("t1", progressJID, "<status>b</status>the answer")
	if last := dl.sends[len(dl.sends)-1]; last.text != "the answer" {
		t.Fatalf("a failed progress edit withheld the reply: %+v", dl.sends)
	}
}

func TestProgress_FirstSendOwedThenRewrittenNotDuplicated(t *testing.T) {
	dl := &recDeliverer{failSend: true}
	db, srv := newProgressTurn(t, dl, "")

	for _, s := range []string{"first", "second"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	rows := statusRows(t, db, progressJID)
	if len(rows) != 1 || rows[0].Content != "⏳ second" {
		t.Fatalf("rows=%+v, want one owed row reading %q", rows, "⏳ second")
	}
	if len(dl.sends) != 1 || len(dl.edits) != 0 {
		t.Fatalf("sends=%d edits=%d, want the one failed send and no edit", len(dl.sends), len(dl.edits))
	}
}

func TestProgress_DelegatedTurnEditsAtTheReturnTarget(t *testing.T) {
	dl := &recDeliverer{}
	const origin = "slack:T/ORIGIN/U"
	db, srv := newProgressTurn(t, dl, origin)

	for _, s := range []string{"a", "b"} {
		if err := srv.mcpSubmitStatus("t1", s); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 1 || len(dl.edits) != 1 || dl.edits[0].jid != origin {
		t.Fatalf("sends=%+v edits=%+v, want one send and one edit at %s", dl.sends, dl.edits, origin)
	}
	if rows := statusRows(t, db, origin); len(rows) != 1 || rows[0].Content != "⏳ b" {
		t.Fatalf("rows at the return target = %+v", rows)
	}
}

func TestProgress_IsTheOnlyProgressWriter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f == "progress.go" || strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, banned := range []*regexp.Regexp{
			regexp.MustCompile(`verbStatus|progressMark|Verb = "status"`),
			regexp.MustCompile(`\b(deliver|dl)\.Typing\(`),
		} {
			if m := banned.Find(src); m != nil {
				t.Errorf("%s holds %q: progress is declared in progress.go only", f, m)
			}
		}
	}
}

func TestChanDeliverer_CanEditReadsTheCapabilityMap(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "1")
	a := newFakeAdapter(t, "pid")
	reg := chanreg.New()
	for name, caps := range map[string]map[string]bool{
		"editd":  {"send_text": true, "edit": true},
		"plaind": {"send_text": true},
	} {
		prefix := name + ":"
		if _, err := reg.Register(name, a.srv.URL, []string{prefix}, caps); err != nil {
			t.Fatal(err)
		}
	}
	d := newChanDeliverer(reg, []string{"muted"}, nil)

	for jid, want := range map[string]bool{
		"editd:1":  true,
		"plaind:1": false,
		"nobody:1": false,
		"muted:1":  false,
	} {
		if got := d.CanEdit(jid); got != want {
			t.Errorf("CanEdit(%s) = %v, want %v", jid, got, want)
		}
	}
}

// progressWithRow opens a turn, streams one status and returns the row it made.
func progressWithRow(t *testing.T, dl *recDeliverer) (*DB, *Server) {
	t.Helper()
	db, srv := newProgressTurn(t, dl, "")
	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	if rows := statusRows(t, db, progressJID); len(rows) != 1 || rows[0].PlatformID == "" {
		t.Fatalf("setup: want one delivered status row, got %+v", rows)
	}
	return db, srv
}

func TestProgress_EndEditsTerminalDone(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := progressWithRow(t, dl)

	if _, err := srv.recordTurnResult("t1", apiv1.TurnResult{TurnID: "t1", SessionID: "s", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if len(dl.edits) != 1 || dl.edits[0].content != "✓ working" {
		t.Fatalf("edits=%+v, want one edit reading %q", dl.edits, "✓ working")
	}
	if rows := statusRows(t, db, progressJID); rows[0].Content != "✓ working" {
		t.Fatalf("row after the edit = %q, want %q", rows[0].Content, "✓ working")
	}
	if tc, _ := db.GetTurnContext("t1"); tc.State != store.TurnDone {
		t.Fatalf("turn state = %q, want done", tc.State)
	}
	if len(dl.typings) != 1 || dl.typings[0] != (typingEvent{progressJID, false}) {
		t.Fatalf("typings=%+v, want one off on %s", dl.typings, progressJID)
	}
}

func TestProgress_EndEditsTerminalFailed(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)

	finishTurn(db, dl, "t1", endFailed)
	if len(dl.edits) != 1 || dl.edits[0].content != "✗ working" {
		t.Fatalf("edits=%+v, want one edit reading %q", dl.edits, "✗ working")
	}
	if rows := statusRows(t, db, progressJID); rows[0].Content != "✗ working" {
		t.Fatalf("row after the edit = %q", rows[0].Content)
	}
}

func TestProgress_EndWithoutRowEditsNothing(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := newProgressTurn(t, dl, "")

	finishTurn(db, dl, "t1", endDone)
	if len(dl.edits) != 0 || len(dl.sends) != 0 {
		t.Fatalf("a turn that reported nothing posted: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
	if len(dl.typings) != 1 || dl.typings[0].on {
		t.Fatalf("typings=%+v, want one off", dl.typings)
	}
}

func TestProgress_EndFansOutToOwnedChats(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	const other = "slack:T/OTHER/U"
	if _, err := db.PutTurnContext("t2", "demo", "", other, "u2", ""); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"t1", "t2"} {
		if err := db.SetTurnRunID(id, "run-1"); err != nil {
			t.Fatal(err)
		}
	}

	finishTurn(db, dl, "t1", endDone)
	off := map[string]bool{}
	for _, e := range dl.typings {
		if e.on {
			t.Fatalf("End turned presence on: %+v", dl.typings)
		}
		off[e.jid] = true
	}
	if !off[progressJID] || !off[other] {
		t.Fatalf("presence cleared on %v, want both %s and %s", off, progressJID, other)
	}
	if len(dl.edits) != 1 || dl.edits[0].jid != progressJID {
		t.Fatalf("edits=%+v, want one edit, on the chat that has a row", dl.edits)
	}
}

func TestProgress_SecondEndEditsOnce(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)

	finishTurn(db, dl, "t1", endDone)
	finishTurn(db, dl, "t1", endFailed)
	if len(dl.edits) != 1 || dl.edits[0].content != "✓ working" {
		t.Fatalf("edits=%+v, want the one ✓ edit and nothing after it", dl.edits)
	}
}

func TestProgress_SteeredEndOnlyMarksTheTurnDone(t *testing.T) {
	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)

	finishTurn(db, dl, "t1", endSteered)
	if len(dl.edits) != 0 || len(dl.typings) != 0 {
		t.Fatalf("a steered turn ended the live run's progress: edits=%+v typings=%+v", dl.edits, dl.typings)
	}
	if tc, _ := db.GetTurnContext("t1"); tc.State != store.TurnDone {
		t.Fatalf("turn state = %q, want done", tc.State)
	}
}

func TestProgress_FailedTerminalEditStillFinishesTheTurn(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	dl := &recDeliverer{}
	db, _ := progressWithRow(t, dl)
	dl.editErr = errSend

	finishTurn(db, dl, "t1", endDone)
	if tc, _ := db.GetTurnContext("t1"); tc.State != store.TurnDone {
		t.Fatalf("a failed edit left the turn %q, want done", tc.State)
	}
	if rows := statusRows(t, db, progressJID); rows[0].Content != "⏳ working" {
		t.Fatalf("row = %q, want it to keep what the user still sees", rows[0].Content)
	}
	if !strings.Contains(logs.String(), "level=ERROR") || !strings.Contains(logs.String(), "turn_id=t1") {
		t.Fatalf("the failed edit was not logged at Error with the turn: %s", logs.String())
	}
}

func TestProgress_UpdateAfterEndIsDropped(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := progressWithRow(t, dl)
	finishTurn(db, dl, "t1", endDone)

	if err := srv.mcpSubmitStatus("t1", "late"); err != nil {
		t.Fatal(err)
	}
	if len(dl.edits) != 1 || len(dl.sends) != 1 {
		t.Fatalf("a status after the end reached the platform: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
	if rows := statusRows(t, db, progressJID); rows[0].Content != "✓ working" {
		t.Fatalf("row = %q, want it to stay terminal", rows[0].Content)
	}
}

// reportingRunner streams one status under the dispatched turn, then ends the
// way mode says: "ok" records a result, "silent" delivers nothing, "error"
// fails.
type reportingRunner struct {
	srv  *Server
	mode string
}

func (r *reportingRunner) Run(_ context.Context, req runedv1.RunRequest) (runedv1.RunOutcome, error) {
	if err := r.srv.mcpSubmitStatus(req.TurnID, "building"); err != nil {
		return runedv1.RunOutcome{}, err
	}
	switch r.mode {
	case "ok":
		_, _ = r.srv.db.RecordTurnResult(string(req.Folder), req.TurnID, "s", "success", "")
		return runedv1.RunOutcome{RunID: "run-r", Outcome: runedv1.OutcomeOK}, nil
	case "error":
		return runedv1.RunOutcome{RunID: "run-r", Outcome: runedv1.OutcomeError, Error: "boom"}, nil
	}
	return runedv1.RunOutcome{RunID: "run-r", Outcome: runedv1.OutcomeOK}, nil
}

func TestProgress_DispatchEndsTheMessageByOutcome(t *testing.T) {
	for _, tc := range []struct {
		mode, mark string
		notice     string
	}{
		{"ok", "✓ building", ""},
		{"silent", "✗ building", silentTurnNotice},
		{"error", "✗ building", runFailureNotice},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			dl := &recDeliverer{}
			runner := &reportingRunner{mode: tc.mode}
			db, srv := newTypingRoutd(t, runner, dl, -1)
			runner.srv = srv
			const jid = "slack:T/C/P1"
			seedTypingMsg(t, db, srv, jid)

			if _, err := srv.loop.processGroupMessages(jid); err != nil {
				t.Fatal(err)
			}
			rows := statusRows(t, db, jid)
			if len(rows) != 1 || rows[0].Content != tc.mark {
				t.Fatalf("status rows = %+v, want one reading %q", rows, tc.mark)
			}
			if len(dl.edits) != 1 || dl.edits[0].content != tc.mark {
				t.Fatalf("edits=%+v, want one edit reading %q", dl.edits, tc.mark)
			}
			sent := dl.sentMsgs()
			if tc.notice == "" {
				if len(sent) != 1 {
					t.Fatalf("sends=%+v, want only the status message", sent)
				}
				return
			}
			if len(sent) != 2 || sent[1].text != tc.notice {
				t.Fatalf("sends=%+v, want the status message and then %q as its own message", sent, tc.notice)
			}
		})
	}
}

func TestProgress_RecoveredTurnEditsItsRowWhenItCompletes(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	before := &recDeliverer{}
	if _, err := db.PutTurnContext("t1", "demo", "", progressJID, "u1", ""); err != nil {
		t.Fatal(err)
	}
	if err := NewServer(db, nil, before, nil, 0, "").mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}

	after := &recDeliverer{}
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: after})
	var mu sync.Mutex
	var recovered []string
	loop.q.SetProcessMessagesFn(func(jid string) (bool, error) {
		mu.Lock()
		recovered = append(recovered, jid)
		mu.Unlock()
		return true, nil
	})
	loop.recoverPending()
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		n := len(recovered)
		mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recoverPending did not re-feed the running turn's chat")
		}
		time.Sleep(10 * time.Millisecond)
	}

	srv := NewServer(db, loop, after, nil, 0, "")
	if _, err := srv.recordTurnResult("t1", apiv1.TurnResult{TurnID: "t1", SessionID: "s", Status: "success"}); err != nil {
		t.Fatal(err)
	}
	if len(after.edits) != 1 || after.edits[0].content != "✓ working" {
		t.Fatalf("edits after the restart = %+v, want one ✓ edit of the persisted message", after.edits)
	}
	if want := before.sends[0]; after.edits[0].jid != want.jid {
		t.Fatalf("edit went to %s, want %s", after.edits[0].jid, want.jid)
	}
}

func TestFinishTurn_IsTheOnlyDoneWriter(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	writer := regexp.MustCompile(`SetTurnState\([^)]*(?:"done"|TurnDone)`)
	var sites []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for range writer.FindAll(src, -1) {
			sites = append(sites, f)
		}
	}
	if len(sites) != 1 || sites[0] != "progress.go" {
		t.Fatalf("a turn is marked done at %v, want finishTurn in progress.go alone", sites)
	}
}
