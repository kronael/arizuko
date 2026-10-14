package routd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

func TestDisengageSurvivesTrailingReply(t *testing.T) {
	for _, topic := range []string{"", "thread-1"} {
		for _, path := range []string{"prose", "tool", "new-thread"} {
			t.Run(topic+"/"+path, func(t *testing.T) {
				db, err := OpenMem()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				const jid = "slack:T/C"
				const folder = "eng"
				if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
					t.Fatal(err)
				}
				srv := NewServer(db, nil, &recDeliverer{}, nil, time.Hour, "")
				if _, err := db.PutTurnContext("old", folder, topic, jid, "user", ""); err != nil {
					t.Fatal(err)
				}
				fns := srv.buildStoreFns(turnMCP{folder: folder, topic: topic, chatJID: jid, turnID: "old", trigger: "user"})
				if err := fns.SetEngagement(jid, topic, folder, time.Time{}); err != nil {
					t.Fatal(err)
				}
				destTopic := topic
				switch path {
				case "prose":
					srv.appendAndDeliver("old", jid, "summary")
				default:
					if path == "new-thread" && topic == "" {
						destTopic = "new-thread"
					}
					if err := fns.SetLastReply(jid, destTopic, "answer", folder); err != nil {
						t.Fatal(err)
					}
					if err := fns.BumpEngagement(jid, destTopic, folder, time.Now().Add(time.Hour)); err != nil {
						t.Fatal(err)
					}
				}
				if _, live := db.Engaged(jid, topic); live {
					t.Fatal("trailing answer reopened disengaged conversation")
				}
				if _, live := db.Engaged(jid, destTopic); live {
					t.Fatal("trailing answer opened a new thread after disengage")
				}
				if _, err := db.PutTurnContext("fresh", folder, topic, jid, "user", ""); err != nil {
					t.Fatal(err)
				}
				srv.appendAndDeliver("fresh", jid, "fresh mention answer")
				if owner, live := db.Engaged(jid, topic); !live || owner != folder {
					t.Fatalf("fresh turn engagement = %q,%v", owner, live)
				}
			})
		}
	}
}

func TestMemberCanDisengageOwnConversation(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const folder = "eng"
	const jid = "slack:T/C"
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: folder}})
	if _, err := db.PutTurnContext("turn", folder, "", jid, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEngagement(jid, "", folder, time.Hour); err != nil {
		t.Fatal(err)
	}
	const foreign = "slack:T/OTHER"
	if err := db.SetEngagement(foreign, "", "other", time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := db.SetTurnTriggerMsg("turn", "thread-root"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetChatIsGroup(jid, true); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, &recDeliverer{pid: "answer"}, nil, time.Hour, "")
	ipcDir := filepath.Join(t.TempDir(), "ipc")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: folder, chatJID: jid, turnID: "turn", trigger: "user"}, ipcDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	sock := groupfolder.IpcSocket(ipcDir)
	names := listToolNames(t, sock)
	if !names["disengage"] {
		t.Fatal("role:member cannot see disengage")
	}
	if names["engage"] {
		t.Fatal("role:member gained unsolicited engage access")
	}
	if _, errText := callToolOverSock(t, sock, "disengage", map[string]any{"jid": foreign}); errText == "" {
		t.Fatal("member cleared a foreign conversation")
	}
	if _, live := db.Engaged(foreign, ""); !live {
		t.Fatal("foreign conversation changed")
	}
	if _, errText := callToolOverSock(t, sock, "reply", map[string]any{"chatJid": jid, "text": "answer before disengage"}); errText != "" {
		t.Fatal(errText)
	}
	if _, errText := callToolOverSock(t, sock, "disengage", map[string]any{"jid": jid}); errText != "" {
		t.Fatal(errText)
	}
	if _, errText := callToolOverSock(t, sock, "reply", map[string]any{"chatJid": jid, "text": "one-shot answer"}); errText != "" {
		t.Fatal(errText)
	}
	srv.appendAndDeliver("turn", jid, "final answer")
	for _, topic := range []string{"", "thread-root"} {
		if _, live := db.Engaged(jid, topic); live {
			t.Fatalf("answer cancelled member disengage on %q", topic)
		}
	}
}

func TestMemberDisengageRequiresOwnedConversation(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	const folder = "eng"
	const jid = "slack:T/C"
	const foreign = "slack:T/FOREIGN"
	if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: "other"}, {Match: "chat_jid=" + foreign, Target: "other"}})
	if _, err := db.PutTurnContext("turn", folder, "", jid, "user", ""); err != nil {
		t.Fatal(err)
	}
	if err := db.SetEngagement(foreign, "", "other", time.Hour); err != nil {
		t.Fatal(err)
	}
	srv := NewServer(db, nil, nil, nil, time.Hour, "")
	binding := turnMCP{folder: folder, chatJID: jid, turnID: "turn", trigger: "user"}
	ipcDir := filepath.Join(t.TempDir(), "ipc")
	stop, err := srv.ServeTurnMCP(binding, ipcDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(stop)
	sock := groupfolder.IpcSocket(ipcDir)
	if _, errText := callToolOverSock(t, sock, "disengage", map[string]any{"jid": foreign, "topic": "thread"}); errText == "" {
		t.Fatal("member cleared a fresh foreign thread")
	}
	if stopped, err := db.store().ThreadDisengaged(foreign, "thread"); err != nil || stopped {
		t.Fatalf("foreign thread changed: stopped=%v err=%v", stopped, err)
	}
	if _, errText := callToolOverSock(t, sock, "disengage", map[string]any{"jid": jid}); errText != "" {
		t.Fatalf("active source cannot stop before first answer: %s", errText)
	}
	fns := srv.buildStoreFns(binding)
	if !fns.JIDRoutedToFolder(jid, folder) {
		t.Fatal("active source binding missing")
	}
	if fns.JIDRoutedToFolder(foreign, folder) {
		t.Fatal("active-source ownership widened to foreign chat")
	}
	if fns.JIDRoutedToFolder(jid, "other") != db.JIDRoutedToFolder(jid, "other") {
		t.Fatal("backend changed another folder's route ownership")
	}
}
