package routd

import (
	"testing"
	"time"

	"github.com/kronael/arizuko/ipc"
)

type existingThreadDeliverer struct{ *recDeliverer }

func (existingThreadDeliverer) HasCapability(_, capability string) bool {
	return capability == "edit" || capability == "thread_id"
}

func TestTaskSummaryUsesAnExistingThreadWithoutCreatingOne(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	srv.deliver = existingThreadDeliverer{dl}
	if _, err := db.PutTurnContext("forum", "demo", "42", progressJID, "u1", ""); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []ipc.StatusKind{ipc.StatusSummary, ipc.StatusProgress} {
		if err := srv.submitStatus(ipc.TurnStatus{TurnID: "forum", Text: "the task", Kind: kind}); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := srv.mcpDeliver("forum", progressJID, "the answer", "", "answer-id", true); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 3 {
		t.Fatalf("sends=%+v", dl.sends)
	}
	for _, send := range dl.sends {
		if send.threadID != "42" || send.threadRoot != "" {
			t.Fatalf("existing thread changed or nested: %+v", send)
		}
	}
}

func TestTaskSummaryWithoutNewThreadSupportKeepsOneProgressCard(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	srv.deliver = existingThreadDeliverer{dl}
	declareTask(t, srv)
	if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "☐ verify", Kind: ipc.StatusProgress}); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 1 || len(dl.edits) != 1 {
		t.Fatalf("unsupported new thread got extra messages: sends=%+v edits=%+v", dl.sends, dl.edits)
	}
	if _, exists := db.MessageByID(progressJID, summaryID("t1")); exists {
		t.Fatal("unsupported new thread created a separate opener")
	}
}

func TestTaskSummaryDelegationPreservesExplicitForeignTarget(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	if _, err := db.PutTurnContext("delegated", "demo", "42", progressJID, "u1", "slack:T/C/origin"); err != nil {
		t.Fatal(err)
	}
	const foreign = "slack:T/C/foreign"
	if _, _, err := srv.mcpDeliver("delegated", foreign, "explicit destination", "", "foreign-id", false); err != nil {
		t.Fatal(err)
	}
	if dl.sends[0].jid != foreign || dl.sends[0].threadID != "" || dl.sends[0].threadRoot != "" {
		t.Fatalf("explicit destination inherited delegation or thread: %+v", dl.sends[0])
	}
	fns := srv.buildGatedFns(turnMCP{turnID: "delegated"})
	if _, topic, err := fns.SendDocument(foreign, "/tmp/report.pdf", "report.pdf", "report", "", "42", "file-id"); err != nil || topic != "" {
		t.Fatalf("foreign file inherited the active topic: topic=%q err=%v", topic, err)
	}
	tts, _ := ttsStub(t)
	srv.SetTTS(TTSConfig(true, tts.URL, "voice", "model", time.Second, t.TempDir()))
	if _, topic, err := fns.SendVoice(foreign, "spoken result", "", "demo", "42"); err != nil || topic != "" {
		t.Fatalf("foreign voice inherited the active topic: topic=%q err=%v", topic, err)
	}
	if dl.docs[0].jid != foreign || dl.docs[0].threadID != "" || dl.docs[0].threadRoot != "" ||
		dl.voices[0].jid != foreign || dl.voices[0].threadID != "" || dl.voices[0].threadRoot != "" {
		t.Fatal("an explicit attachment destination inherited delegation or thread")
	}
}
