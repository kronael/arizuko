package routd

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// noticeFailingDeliverer refuses every notice a turn owes and delivers the rest.
type noticeFailingDeliverer struct{ *recDeliverer }

func (d *noticeFailingDeliverer) Send(jid, text, replyTo, threadID, threadRoot, idem string) (string, error) {
	for _, prefix := range []string{"status-"} {
		if strings.HasPrefix(idem, prefix) {
			_, _ = d.recDeliverer.Send(jid, text, replyTo, threadID, threadRoot, idem)
			return "", errSend
		}
	}
	return d.recDeliverer.Send(jid, text, replyTo, threadID, threadRoot, idem)
}

func captureWarnings(t *testing.T) *syncBuf {
	t.Helper()
	buf := &syncBuf{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

func assertNoticeLogged(t *testing.T, logs *syncBuf, jid string) {
	t.Helper()
	var line string
	for _, l := range strings.Split(logs.String(), "\n") {
		if strings.Contains(l, "status operation queued") {
			line = l
		}
	}
	if line == "" {
		t.Fatalf("no WARN for the failed notice send; log:\n%s", logs.String())
	}
	for _, want := range []string{"level=WARN", "jid=" + jid, "turn_id=", "err="} {
		if !strings.Contains(line, want) {
			t.Fatalf("notice WARN %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "turn_id= ") || strings.HasSuffix(line, "turn_id=") {
		t.Fatalf("notice WARN %q has an empty turn id", line)
	}
}

func TestNoticeSend_FailureIsLogged(t *testing.T) {
	t.Run("ghost group", func(t *testing.T) {
		logs := captureWarnings(t)
		db, err := OpenMem()
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		dl := &noticeFailingDeliverer{&recDeliverer{}}
		loop := NewLoop(db, runnerFn(func(context.Context, runedv1.RunRequest) (runedv1.RunOutcome, error) {
			return runedv1.RunOutcome{Outcome: runedv1.OutcomeOK}, nil
		}), LoopConfig{Deliver: dl})
		loop.StopQueue()
		doSetRoutes(t, db, []core.Route{{Match: "platform=slack", Target: "ghost"}})
		_ = db.PutMessage(core.Message{ID: "g1", ChatJID: "slack:T/C/U", Sender: "u1",
			Content: "hi", Timestamp: time.Now().UTC(), Verb: "message"})

		_, _ = loop.processGroupMessages("slack:T/C/U")

		if sent := dl.sentMsgs(); len(sent) != 1 || sent[0].text != renderTurnStatus(turnStatus{phase: "setup"}) {
			t.Fatalf("sends=%+v, want the ghost-group notice", sent)
		}
		assertNoticeLogged(t, logs, "slack:T/C/U")
	})

	t.Run("budget", func(t *testing.T) {
		logs := captureWarnings(t)
		db, loop, _, fake := gatewayLoop(t, "demo")
		dl := &noticeFailingDeliverer{&recDeliverer{}}
		loop.deliver = dl
		if err := db.SetFolderCap("demo", 1000); err != nil {
			t.Fatal(err)
		}
		seedUser(t, db, "google:dave", 30)
		fake.setTeam("demo", 0, 1000)
		fake.setUser("google:dave", 30, 30)
		_ = db.PutMessage(core.Message{ID: "b", ChatJID: "web:demo", Sender: "google:dave",
			Content: "expensive question", Timestamp: time.Now().UTC()})

		if _, err := loop.processGroupMessages("web:demo"); err != nil {
			t.Fatal(err)
		}
		if len(dl.sentMsgs()) != 1 {
			t.Fatalf("sends=%+v, want the budget refusal", dl.sentMsgs())
		}
		assertNoticeLogged(t, logs, "web:demo")
	})

	for _, tc := range []struct{ mode, notice string }{
		{"silent", renderTurnStatus(turnStatus{phase: "silent"})},
		{"error", renderTurnStatus(turnStatus{phase: "final", cause: "Agent could not complete the request"})},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			logs := captureWarnings(t)
			dl := &noticeFailingDeliverer{&recDeliverer{}}
			runner := &reportingRunner{mode: tc.mode}
			db, srv := newTypingRoutd(t, runner, dl, -1)
			runner.srv = srv
			const jid = "slack:T/C/P1"
			seedTypingMsg(t, db, srv, jid)

			if _, err := srv.loop.processGroupMessages(jid); err != nil {
				t.Fatal(err)
			}
			var tried bool
			for _, m := range dl.sentMsgs() {
				tried = tried || m.text == tc.notice
			}
			row, ok := db.LastStatusRow(jid, "tm1")
			if !tried && (!ok || row.Content != tc.notice) {
				t.Fatalf("sends=%+v status=%+v, want the %s notice owed", dl.sentMsgs(), row, tc.mode)
			}
			assertNoticeLogged(t, logs, jid)
		})
	}
}
