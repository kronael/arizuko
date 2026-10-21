package routd

import (
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
)

func summaryBroker(t *testing.T, dl *recDeliverer) (*DB, *Server, string) {
	t.Helper()
	db, srv := newSummaryTurn(t, dl)
	if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "demo"}})
	bound := turnMCP{folder: "demo", chatJID: progressJID, turnID: "t1", trigger: "u1"}
	sock := t.TempDir() + "/gated.sock"
	stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound), srv.buildStoreFns(bound), "demo", true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	return db, srv, sock
}

func declareTask(t *testing.T, srv *Server) {
	t.Helper()
	if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "verify the release", Kind: ipc.StatusSummary}); err != nil {
		t.Fatal(err)
	}
}

func TestTaskSummaryPlacesRealProgressAndAllAnswersInItsThread(t *testing.T) {
	dl := &recDeliverer{}
	db, srv, sock := summaryBroker(t, dl)
	declareTask(t, srv)
	opener, _ := db.MessageByID(progressJID, summaryID("t1"))
	if err := srv.submitStatus(ipc.TurnStatus{TurnID: "t1", Text: "☐ build\n☐ test", Kind: ipc.StatusProgress}); err != nil {
		t.Fatal(err)
	}
	if _, msg := callToolOverSock(t, sock, "reply", map[string]any{"chatJid": progressJID, "text": "the result"}); msg != "" {
		t.Fatal(msg)
	}
	if _, thread, err := srv.mcpAppendDoc("t1", progressJID, "/tmp/report.pdf", "report.pdf", "report", "", "", "file-1"); err != nil || thread != opener.PlatformID {
		t.Fatalf("document thread=%q err=%v", thread, err)
	}
	tts, _ := ttsStub(t)
	srv.SetTTS(TTSConfig(true, tts.URL, "voice", "model", time.Second, t.TempDir()))
	if _, thread, err := srv.mcpSendVoice("t1", progressJID, "spoken result", "", "demo", ""); err != nil || thread != opener.PlatformID {
		t.Fatalf("voice thread=%q err=%v", thread, err)
	}
	tc, _ := db.GetTurnContext("t1")
	if err := srv.sendTurnNotice(tc, timedOutNotice); err != nil {
		t.Fatal(err)
	}
	if len(dl.sends) != 4 || dl.sends[0].threadRoot != "" || dl.sends[0].threadID != "" {
		t.Fatalf("opener or child count changed: %+v", dl.sends)
	}
	for _, send := range dl.sends[1:] {
		if send.threadRoot != opener.PlatformID || send.threadID != "" {
			t.Fatalf("child escaped the opener thread: %+v", send)
		}
	}
	if dl.docs[0].threadRoot != opener.PlatformID || dl.voices[0].threadRoot != opener.PlatformID {
		t.Fatal("an attachment lost the opener root")
	}
	rows, err := db.MessagesSince(progressJID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Content == "the result" && row.Topic != opener.PlatformID {
			t.Fatalf("MCP answer lost its durable thread: %+v", row)
		}
	}
}

func TestTaskSummaryPendingOpenerCannotQueueAParentReply(t *testing.T) {
	dl := &recDeliverer{failIdem: summaryID("t1")}
	db, srv, sock := summaryBroker(t, dl)
	declareTask(t, srv)
	for _, tool := range []string{"send", "reply"} {
		if _, msg := callToolOverSock(t, sock, tool, map[string]any{"chatJid": progressJID, "text": "the result"}); !strings.Contains(msg, "not delivered, and no retry follows") {
			t.Fatalf("unresolved opener retained an unplaced child: %q", msg)
		}
	}
	rows, err := db.PendingOutbound(time.Now().Add(time.Hour), 10)
	if err != nil || len(rows) != 1 || rows[0].ID != summaryID("t1") {
		t.Fatalf("only the opener may be pending: rows=%+v err=%v", rows, err)
	}
	dl.failIdem = ""
	loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
	loop.maybeRetryOutbound(time.Now().Add(time.Hour))
	for _, send := range dl.sends {
		if send.text == "the result" {
			t.Fatal("outbound replay sent a child that the tool refused")
		}
		if send.idem != summaryID("t1") {
			t.Fatal("opener retry changed its idempotency key")
		}
	}
	if _, msg := callToolOverSock(t, sock, "reply", map[string]any{"chatJid": progressJID, "text": "recovered result"}); msg != "" {
		t.Fatal(msg)
	}
	opener, _ := db.MessageByID(progressJID, summaryID("t1"))
	if dl.sends[len(dl.sends)-1].threadRoot != opener.PlatformID {
		t.Fatal("recovery escaped the now-delivered opener")
	}
}

func TestTaskSummaryPendingChildReplaysItsRootAndIdentity(t *testing.T) {
	dl := &recDeliverer{}
	db, srv, sock := summaryBroker(t, dl)
	declareTask(t, srv)
	dl.failSend = true
	if _, msg := callToolOverSock(t, sock, "reply", map[string]any{"chatJid": progressJID, "text": "the result"}); !strings.Contains(msg, "queued") {
		t.Fatal(msg)
	}
	failed := dl.sends[len(dl.sends)-1]
	dl.failSend = false
	NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl}).maybeRetryOutbound(time.Now().Add(time.Hour))
	replayed := dl.sends[len(dl.sends)-1]
	if failed.threadRoot == "" || replayed.threadRoot != failed.threadRoot || replayed.idem != failed.idem || replayed.threadID != "" {
		t.Fatalf("saved topic lost thread creation/reuse: failed=%+v replay=%+v", failed, replayed)
	}
}
