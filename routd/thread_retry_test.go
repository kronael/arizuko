package routd

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/ipc"
)

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
			if first.jid != destination || first.threadRoot != root ||
				first.threadID != test.topic {
				t.Fatalf("initial placement: %+v", first)
			}
			now := time.Now().Add(time.Hour)
			rows, err := db.PendingOutbound(now, 10)
			if err != nil || len(rows) != 1 || rows[0].ID != first.idem {
				t.Fatalf("pending reply: rows=%+v err=%v", rows, err)
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
