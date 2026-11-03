package routd

import (
	"testing"
	"time"
)

func TestDelegationPreservesExplicitForeignTarget(t *testing.T) {
	dl := &recDeliverer{}
	db, srv := newProgressTurn(t, dl, "")
	if _, err := db.PutTurnContext("delegated", "demo", "42", progressJID, "u1", "slack:T/C/origin"); err != nil {
		t.Fatal(err)
	}
	if _, topic, err := srv.mcpDeliver("delegated", progressJID, "parent notice", "", "send-id", false); err != nil || topic != "" {
		t.Fatalf("explicit send topic=%q err=%v", topic, err)
	}
	if _, topic, err := srv.mcpDeliver("delegated", progressJID, "answer", "", "reply-id", true); err != nil || topic != "42" {
		t.Fatalf("delegated reply topic=%q err=%v", topic, err)
	}
	if dl.sends[0].jid != progressJID || dl.sends[0].threadID != "" || dl.sends[0].threadRoot != "" ||
		dl.sends[1].jid != "slack:T/C/origin" || dl.sends[1].threadID != "42" {
		t.Fatalf("delegated destinations: %+v", dl.sends)
	}
	const foreign = "slack:T/C/foreign"
	if _, _, err := srv.mcpDeliver("delegated", foreign, "explicit destination", "", "foreign-id", false); err != nil {
		t.Fatal(err)
	}
	if dl.sends[2].jid != foreign || dl.sends[2].threadID != "" || dl.sends[2].threadRoot != "" {
		t.Fatalf("explicit destination inherited delegation or thread: %+v", dl.sends[2])
	}
	fns := srv.buildRoutdFns(turnMCP{turnID: "delegated"})
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
