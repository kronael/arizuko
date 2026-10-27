package routd

import (
	"testing"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func TestTimeoutAfterReplyRecordsFailureAndNotifiesOnce(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	srv.replyForTest(t, "t1", progressJID, "work continues in the background")
	for range 2 {
		_, err := srv.recordTurnResult("t1", apiv1.TurnResult{
			TurnID: "t1", Status: "success", TimedOut: true,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.store().GetTurnResult("demo", "t1")
	if err != nil || result.Status != "error" || db.TurnFailure("demo", "t1") != timedOutNotice {
		t.Fatalf("timeout completion = %+v, %v", result, err)
	}
	row, ok := db.LastStatusRow(progressJID, "t1")
	if !ok || row.Content != "✗ working" {
		t.Fatalf("timeout progress = %+v, found=%v", row, ok)
	}
	notices := 0
	for _, send := range dl.sends {
		if send.text == timedOutNotice {
			notices++
		}
	}
	if notices != 1 {
		t.Fatalf("timeout notices = %d, want one", notices)
	}
}

func TestTimeoutAfterReplyKeepsRecordedCompletion(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	srv.replyForTest(t, "t1", progressJID, "finished")
	for _, timedOut := range []bool{false, true} {
		_, err := srv.recordTurnResult("t1", apiv1.TurnResult{
			TurnID: "t1", Status: "success", TimedOut: timedOut,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	result, err := db.store().GetTurnResult("demo", "t1")
	if err != nil || result.Status != "success" || db.TurnFailure("demo", "t1") != "" {
		t.Fatalf("recorded completion = %+v, %v", result, err)
	}
	for _, send := range dl.sentMsgs() {
		if send.text == timedOutNotice {
			t.Fatal("late timeout sent a notice")
		}
	}
}
