package routd

import (
	"testing"

	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

func newSummaryTurn(t *testing.T, dl *recDeliverer) (*DB, *Server) {
	t.Helper()
	dl.canThread = true
	db, srv := newProgressTurn(t, dl, "")
	if err := db.SetChatIsGroup(progressJID, true); err != nil {
		t.Fatal(err)
	}
	return db, srv
}

func TestTaskSummaryIsImmutableAndOwedWithoutDelivery(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newSummaryTurn(t, dl)
	for _, text := range []string{"review the release", "changed title"} {
		if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: text, Kind: ipc.StatusSummary}); err != nil {
			t.Fatal(err)
		}
	}
	row, ok := db.MessageByID(progressJID, summaryID("t1"))
	if !ok || row.Content != "🤖 review the release" || row.Verb != store.VerbStatusSummary {
		t.Fatalf("summary=%+v exists=%v", row, ok)
	}
	if len(dl.sends) != 1 || len(dl.edits) != 0 || db.TurnHasBotReply(progressJID, "t1") {
		t.Fatalf("summary counted as delivery or changed: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
	check, err := srv.mcpCheckTurn("t1")
	if err != nil || !check.Owed || check.Delivered || check.Nudge == "" {
		t.Fatalf("summary must owe an answer: check=%+v err=%v", check, err)
	}
	finishTurn(db, dl, "t1", endFailed)
	row, _ = db.MessageByID(progressJID, summaryID("t1"))
	if row.Content != "🤖 review the release" || len(dl.edits) != 0 {
		t.Fatalf("completion edited the immutable opener: row=%+v edits=%+v", row, dl.edits)
	}
}

func TestTaskSummaryRequiresDeclarationBeforeChecklist(t *testing.T) {
	dl := &recDeliverer{}
	_, srv := newSummaryTurn(t, dl)
	progress := ipc.TurnStatus{TurnID: "t1", Text: "☐ build\n☐ test", Kind: ipc.StatusProgress}
	if err := srv.submitStatus(progress); err == nil || len(dl.sends) != 0 {
		t.Fatal("a first checklist must not become the summary")
	}
	for _, text := range []string{"", "one\ntwo"} {
		if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: text, Kind: ipc.StatusSummary}); err == nil {
			t.Fatal("invalid summary accepted")
		}
	}
	if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "verify the release", Kind: ipc.StatusSummary}); err != nil {
		t.Fatal(err)
	}
	if err := srv.submitStatus(progress); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 2 || dl.sends[1].text != "⏳ "+progress.Text {
		t.Fatalf("expected summary and actual checklist: %+v", dl.sends)
	}
}

func TestTaskSummaryAndChecklistWithoutEditableSinkAreNoOps(t *testing.T) {
	for _, sink := range []string{"none", "noedit", "muted"} {
		dl := &recDeliverer{noEdit: sink == "noedit"}
		db, srv := newProgressTurn(t, dl, "")
		if sink == "none" {
			srv.deliver = nil
		}
		if sink == "muted" {
			srv.SetDisabledGroups([]string{"demo"})
		}
		for _, kind := range []ipc.StatusKind{ipc.StatusSummary, ipc.StatusProgress} {
			if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "the task", Kind: kind}); err != nil {
				t.Fatalf("unsupported progress returned an error: %v", err)
			}
		}
		if _, ok := db.MessageByID(progressJID, summaryID("t1")); ok {
			t.Fatal("unsupported sink stored an opener")
		}
		if len(dl.sends) != 0 || len(dl.edits) != 0 {
			t.Fatal("unsupported sink received progress")
		}
	}
}
