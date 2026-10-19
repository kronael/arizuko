package routd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

func TestMCPMissingTelegramReplyTargetRecovery(t *testing.T) {
	for _, recovery := range []string{"earlier reply", "same chat send", "queued transient"} {
		t.Run(recovery, func(t *testing.T) {
			db, err := OpenMem()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if err := db.PutGroup(core.Group{Folder: "demo"}); err != nil {
				t.Fatal(err)
			}
			doSetRoutes(t, db, []core.Route{{Match: "platform=telegram", Target: "demo"}})
			const jid = "telegram:555"
			if _, err := db.PutTurnContext("turn", "demo", "", jid, "human", ""); err != nil {
				t.Fatal(err)
			}
			if err := db.SetTurnAddressed("turn", true); err != nil {
				t.Fatal(err)
			}
			var requests []chanlib.SendRequest
			var mu sync.Mutex
			sent := func() []chanlib.SendRequest {
				mu.Lock()
				defer mu.Unlock()
				return append([]chanlib.SendRequest(nil), requests...)
			}
			adapter := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req chanlib.SendRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
				}
				mu.Lock()
				requests = append(requests, req)
				mu.Unlock()
				if req.ChatJID != jid {
					t.Error("recovery changed the chat", req.ChatJID)
				}
				if req.ReplyTo == "999" {
					if recovery == "queued transient" {
						w.WriteHeader(500)
					} else {
						w.WriteHeader(400)
					}
					if err := json.NewEncoder(w).Encode(map[string]string{"error": "Bad Request: message to be replied not found"}); err != nil {
						t.Error(err)
					}
					return
				}
				if err := json.NewEncoder(w).Encode(map[string]string{"id": "42"}); err != nil {
					t.Error(err)
				}
			}))
			defer adapter.Close()
			reg := chanreg.New()
			if _, err := reg.Register("telegram", adapter.URL, []string{"telegram:"}, map[string]bool{"send_text": true}); err != nil {
				t.Fatal(err)
			}
			srv := NewServer(db, nil, NewChannelDeliverer(reg, nil, nil), nil, 0, "")
			dir := t.TempDir()
			stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", chatJID: jid, turnID: "turn"}, dir)
			if err != nil {
				t.Fatal(err)
			}
			defer stop()
			_, errText := callToolOverSock(t, groupfolder.IpcSocket(dir), "reply", map[string]any{"chatJid": jid, "text": "answer", "replyToId": "999"})
			if !strings.Contains(errText, "message to be replied not found") {
				t.Fatal("MCP lost Telegram's reason", errText)
			}
			check, err := srv.mcpCheckTurn("turn")
			if err != nil {
				t.Fatal(err)
			}
			pending, err := db.store().CountPendingOutbound(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if recovery == "queued transient" {
				if !strings.Contains(errText, "queued") || !strings.Contains(errText, "do NOT send it again") || !check.Delivered || pending != 1 || len(sent()) != 1 {
					t.Fatalf("router-owned retry lost: error=%q check=%+v pending=%d requests=%+v", errText, check, pending, sent())
				}
				return
			}
			if !strings.Contains(errText, "not delivered, and no retry follows") || check.Delivered || !check.Owed || check.Nudge == "" || pending != 0 {
				t.Fatalf("failed reply falsely satisfied turn: error=%q check=%+v pending=%d", errText, check, pending)
			}
			tool := "reply"
			args := map[string]any{"chatJid": jid, "text": "answer", "replyToId": "55"}
			if recovery == "same chat send" {
				tool = "send"
				delete(args, "replyToId")
			}
			if _, msg := callToolOverSock(t, groupfolder.IpcSocket(dir), tool, args); msg != "" {
				t.Fatal(msg)
			}
			check, err = srv.mcpCheckTurn("turn")
			if err != nil || !check.Delivered || len(sent()) != 2 {
				t.Fatalf("recovery did not complete once: check=%+v requests=%+v err=%v", check, sent(), err)
			}
			wantReply := "55"
			if tool == "send" {
				wantReply = ""
			}
			if sent()[1].ReplyTo != wantReply {
				t.Fatal("recovery target changed", sent()[1])
			}
			pendingRows, err := db.store().PendingOutbound(t.Context(), time.Now().Add(time.Hour), 50)
			if err != nil || len(pendingRows) != 0 {
				t.Fatalf("permanent failure queued duplicate retry: %+v %v", pendingRows, err)
			}
		})
	}
}
