package routd

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
	"github.com/kronael/arizuko/store"
)

func TestDelegatedReplyRetryPreservesDestination(t *testing.T) {
	const origin = "slack:T/C/origin"
	for _, test := range []struct {
		name, tool, topic, jid, wantJID, verb string
		fail                                  bool
	}{
		{"reply-thread", "reply", "th-9", progressJID, origin, "reply", true},
		{"reply-empty", "reply", "", progressJID, origin, "reply", true},
		{"foreign-reply", "reply", "th-9", "slack:T/C/foreign", "slack:T/C/foreign", "reply", true},
		{"send-failure", "send", "th-9", progressJID, progressJID, "message", true},
		{"send-success", "send", "th-9", progressJID, progressJID, "message", false},
		{"reply-success", "reply", "th-9", progressJID, origin, "reply", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dl := &recDeliverer{}
			db, _, sock := summaryBroker(t, dl)
			if _, err := db.PutTurnContext("t1", "demo", test.topic, progressJID, "u1", origin); err != nil {
				t.Fatal(err)
			}
			dl.failSend = test.fail
			_, message := callToolOverSock(t, sock, test.tool, map[string]any{
				"chatJid": test.jid, "text": "delegated answer",
			})
			if test.fail && !strings.Contains(message, "queued") || !test.fail && message != "" {
				t.Fatalf("tool response: %q", message)
			}
			first := dl.sentMsgs()[0]
			wantTopic := ""
			if test.tool == "reply" && test.jid == progressJID {
				wantTopic = test.topic
			}
			if first.jid != test.wantJID || first.threadID != wantTopic || first.threadRoot != "" || first.replyTo != "" {
				t.Fatalf("initial delivery: %+v", first)
			}
			row, ok := db.MessageByID(test.jid, first.idem)
			if !ok || row.ChatJID != test.jid || row.Verb != test.verb || row.Topic != wantTopic {
				t.Fatalf("saved row: %+v, found=%v", row, ok)
			}
			if test.jid == progressJID && !db.TurnHasBotReply(progressJID, "t1") {
				t.Fatal("turn bookkeeping missed outbound row")
			}
			if !test.fail {
				return
			}
			dl.failSend = false
			NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl}).maybeRetryOutbound(time.Now().Add(time.Hour))
			calls := dl.sentMsgs()
			if len(calls) != 2 || !reflect.DeepEqual(calls[1], first) {
				t.Fatalf("retry changed delivery: first=%+v calls=%+v", first, calls)
			}
			if test.jid == progressJID && !db.TurnHasBotReply(progressJID, "t1") {
				t.Fatal("turn bookkeeping missed delivered row")
			}
		})
	}
}

func TestThreadReplyRetryPreservesPlacement(t *testing.T) {
	const jid = "discord:g1/ch-1"
	for _, test := range []struct {
		name, tool, topic, destination, root string
		summary, prefixed                    bool
	}{
		{name: "prefixed-trigger", tool: "reply", root: "human-root", prefixed: true},
		{name: "without-summary", tool: "reply", root: "human-root"},
		{name: "with-summary", tool: "reply", summary: true},
		{name: "existing-thread", tool: "reply", topic: "human-root"},
		{name: "top-level-send", tool: "send"},
		{name: "send-from-existing-thread", tool: "send", topic: "human-root"},
		{name: "send-from-summary", tool: "send", summary: true},
		{name: "different-destination", tool: "reply", destination: "discord:g1/ch-2"},
	} {
		t.Run(test.name, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			dl := &recDeliverer{canThread: true}
			srv := NewServer(db, nil, dl, nil, 0, "")
			if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, []core.Route{{Match: "platform=discord", Target: "demo"}})
			if err := db.SetChatIsGroup(jid, true); err != nil {
				t.Fatal(err)
			}
			_, err = db.PutTurnContext("t1", "demo", test.topic, jid, "u1", "")
			if err != nil {
				t.Fatal(err)
			}
			trigger := "human-root"
			if test.prefixed {
				trigger = jid + "/human-root"
			}
			if err := db.SetTurnTriggerMsg("t1", trigger); err != nil {
				t.Fatal(err)
			}
			root := test.root
			if test.summary {
				declareTask(t, srv)
				opener, ok := db.MessageByID(jid, summaryID("t1"))
				if !ok || opener.PlatformID == "" {
					t.Fatal("task summary did not arrive")
				}
				root = opener.PlatformID
			}
			bound := turnMCP{
				folder: "demo", chatJID: jid, turnID: "t1", trigger: "u1",
				topic: test.topic,
			}
			sock := t.TempDir() + "/gated.sock"
			stop, err := ipc.ServeMCP(sock, srv.buildRoutdFns(bound),
				srv.buildStoreFns(bound), "demo", true, 0, "")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(stop)
			destination := test.destination
			if destination == "" {
				destination = jid
			}
			dl.failSend = true
			_, message := callToolOverSock(t, sock, test.tool,
				map[string]any{"chatJid": destination, "text": "answer"})
			if !strings.Contains(message, "queued") {
				t.Fatalf("transient failure was not queued: %q", message)
			}
			calls := dl.sentMsgs()
			first := calls[len(calls)-1]
			wantThread := test.topic
			if test.tool == "send" {
				root, wantThread = "", ""
			}
			if first.jid != destination || first.threadRoot != root ||
				first.threadID != wantThread {
				t.Fatalf("initial placement: %+v", first)
			}
			now := time.Now().Add(time.Hour)
			rows, err := db.PendingOutbound(now, 10)
			if err != nil || len(rows) != 1 || rows[0].ID != first.idem {
				t.Fatalf("pending reply: rows=%+v err=%v", rows, err)
			}
			if rows[0].Topic != root+wantThread {
				t.Fatalf("saved topic = %q, want %q", rows[0].Topic, root+wantThread)
			}
			if test.tool == "reply" && root != "" {
				off := false
				var ws store.WriteSet
				if err := db.store().SetChatThreadRepliesInto(&ws, jid, &off); err != nil {
					t.Fatal(err)
				}
				if err := db.store().Apply(context.Background(), &ws); err != nil {
					t.Fatal(err)
				}
			}
			dl.failSend = false
			loop := NewLoop(db, &recRunner{}, LoopConfig{Deliver: dl})
			loop.maybeRetryOutbound(now)
			retried := dl.sentMsgs()
			if len(retried) != len(calls)+1 || !reflect.DeepEqual(retried[len(retried)-1], first) {
				t.Fatalf("retry changed placement or identity: first=%+v calls=%+v",
					first, retried)
			}
			rows, err = db.PendingOutbound(now, 10)
			if err != nil || len(rows) != 0 {
				t.Fatalf("delivered reply remains pending: rows=%+v err=%v", rows, err)
			}
			loop.maybeRetryOutbound(now.Add(time.Hour))
			if len(dl.sentMsgs()) != len(retried) {
				t.Fatal("delivered reply was sent again")
			}
		})
	}
}
