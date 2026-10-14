package routd

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

func TestZeroEngagementTTLDoesNotRenewReplies(t *testing.T) {
	for _, path := range []string{"prose", "tool"} {
		for _, stopped := range []bool{false, true} {
			t.Run(path, func(t *testing.T) {
				db, err := OpenMem()
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { db.Close() })
				const folder = "eng"
				const jid = "slack:T/C"
				const topic = "thread"
				if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
					t.Fatal(err)
				}
				doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: folder}})
				if stopped {
					if err := db.store().BumpEngagement(jid, topic, folder, time.Time{}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := db.PutTurnContext("turn", folder, topic, jid, "user", ""); err != nil {
					t.Fatal(err)
				}
				srv := NewServer(db, nil, &recDeliverer{pid: "answer"}, nil, 0, "")
				if path == "prose" {
					srv.appendAndDeliver("turn", jid, "answer")
				} else {
					ipcDir := filepath.Join(t.TempDir(), "ipc")
					stop, err := srv.ServeTurnMCP(turnMCP{folder: folder, chatJID: jid, topic: topic, turnID: "turn", trigger: "user"}, ipcDir)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(stop)
					if stopped {
						if _, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "disengage", map[string]any{"jid": jid, "topic": topic}); errText != "" {
							t.Fatal(errText)
						}
					}
					if _, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "reply", map[string]any{"chatJid": jid, "text": "answer"}); errText != "" {
						t.Fatal(errText)
					}
				}
				if _, live := db.Engaged(jid, topic); live {
					t.Fatal("explicit0 opened automatic engagement")
				}
				if got, err := db.store().ThreadDisengaged(jid, topic); err != nil || got != stopped {
					t.Fatalf("reply changed explicit stop: stopped=%v want=%v err=%v", got, stopped, err)
				}
			})
		}
	}
}

func TestZeroEngagementTTLPreservesExistingLease(t *testing.T) {
	for _, path := range []string{"prose", "tool"} {
		t.Run(path, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { db.Close() })
			const folder = "eng"
			const jid = "slack:T/C"
			const topic = "thread"
			if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, []core.Route{{Match: "chat_jid=" + jid, Target: folder}})
			until := time.Now().Add(30 * time.Minute)
			if err := db.store().BumpEngagement(jid, topic, "original-owner", until); err != nil {
				t.Fatal(err)
			}
			if _, err := db.PutTurnContext("turn", folder, topic, jid, "user", ""); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(db, nil, &recDeliverer{pid: "answer"}, nil, 0, "")
			if path == "prose" {
				srv.appendAndDeliver("turn", jid, "answer")
			} else {
				ipcDir := filepath.Join(t.TempDir(), "ipc")
				stop, err := srv.ServeTurnMCP(turnMCP{folder: folder, chatJID: jid, topic: topic, turnID: "turn", trigger: "user"}, ipcDir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(stop)
				if _, errText := callToolOverSock(t, groupfolder.IpcSocket(ipcDir), "reply", map[string]any{"chatJid": jid, "text": "answer"}); errText != "" {
					t.Fatal(errText)
				}
			}
			lease, live := db.Engagement(jid, topic)
			if !live || lease.Folder != "original-owner" || !lease.Until.Equal(until) {
				t.Fatalf("zero-TTL reply changed existing lease: %+v live=%v want until=%v", lease, live, until)
			}
		})
	}
}
