package routd

import (
	"strings"
	"testing"
)

// A turn that runs inside a platform thread posts its first status inside that
// thread. No agent-written opener exists: every bot row of the turn is a status
// row, and no message starts with the opener mark.
func TestProgress_FirstStatusJoinsTheTurnsTopicWithoutAnOpener(t *testing.T) {
	dl := &recDeliverer{canThread: true}
	db, srv := newProgressTurn(t, dl, "")
	if err := db.SetChatIsGroup(progressJID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.PutTurnContext("threaded", "demo", "th-7", progressJID, "u1", ""); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"adding the delta", "checking the digest"} {
		if err := srv.mcpSubmitStatus("threaded", text); err != nil {
			t.Fatal(err)
		}
	}
	if len(dl.sends) != 1 || dl.sends[0].threadID != "th-7" || dl.sends[0].threadRoot != "" {
		t.Fatalf("the status line left the turn's topic: %+v", dl.sends)
	}
	if strings.Contains(dl.sends[0].text, "🤖") {
		t.Fatalf("an opener was posted: %+v", dl.sends)
	}
	rows, err := db.MessagesSince(progressJID, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.BotMsg && row.Verb != verbStatus {
			t.Fatalf("a progress call stored a row that is not a status line: %+v", row)
		}
	}
}

// Without a topic, the line takes the reply's own placement: a chat with
// thread_replies roots both on the trigger message, so the line and the answer
// share one thread and the line never starts a thread of its own.
func TestProgress_LineAndReplyShareOneThreadRoot(t *testing.T) {
	dl := &recDeliverer{canThread: true}
	db, srv := newProgressTurn(t, dl, "")
	if err := db.SetChatIsGroup(progressJID, true); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTurnTriggerMsg("t1", "root-1"); err != nil {
		t.Fatal(err)
	}
	if err := srv.mcpSubmitStatus("t1", "working"); err != nil {
		t.Fatal(err)
	}
	srv.replyForTest(t, "t1", progressJID, "the answer")
	if len(dl.sends) != 2 || dl.sends[0].threadRoot != "root-1" || dl.sends[1].threadRoot != "root-1" {
		t.Fatalf("line and reply do not share the trigger's thread: %+v", dl.sends)
	}
}
