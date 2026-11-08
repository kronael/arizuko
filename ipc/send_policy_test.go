package ipc

import (
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/store"
)

func TestMain(m *testing.M) { dyntest.Main(m) }

func TestSendGroupRealPolicy(t *testing.T) {
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.PutMembership("folder:world/a", store.RoleMember, "test"); err != nil {
		t.Fatal(err)
	}
	var outbound []outboundRow
	db := captureStore(&outbound)
	db.Authorize = func(target, action string, params map[string]string) bool {
		return auth.Authorize(s, auth.Caller{Principal: "user:alice", Route: "world/a"}, action, target, params)
	}
	var messages []core.Message
	db.PutMessage = func(m core.Message) error { messages = append(messages, m); return nil }
	db.DefaultFolderForJID = func(jid string) string {
		if jid == "telegram:chat/own" {
			return "world/a"
		}
		return "world/b"
	}
	sent := 0
	routd := RoutdFns{SendMessage: func(string, string, string) (string, string, error) {
		sent++
		return "m1", "", nil
	}}
	sock := t.TempDir() + "/send.sock"
	stop, err := ServeMCP(sock, routd, db, "world/a", false, 0, "user:alice")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	_, errText := callTool(t, sock, "send", map[string]any{"chatJid": "world/b", "text": "hi"})
	if errText == "" || len(messages) != 0 {
		t.Fatalf("ungranted group send: error %q, messages %+v", errText, messages)
	}
	_, errText = callTool(t, sock, "send", map[string]any{"chatJid": "telegram:chat/own", "text": "hi"})
	if errText != "" || sent != 1 {
		t.Fatalf("owned platform send: error %q, sent %d", errText, sent)
	}
	_, errText = callTool(t, sock, "send", map[string]any{"chatJid": "telegram:chat/other", "text": "hi"})
	if errText == "" || sent != 1 {
		t.Fatalf("unowned platform send: error %q, sent %d", errText, sent)
	}
	if err := s.PutACLRow(core.ACLRow{Principal: "folder:world/a", Action: "mcp:send", Scope: "world/b", Effect: store.EffectAllow}); err != nil {
		t.Fatal(err)
	}
	_, errText = callTool(t, sock, "send", map[string]any{"chatJid": "world/b", "text": "hi"})
	if errText != "" || len(messages) != 1 {
		t.Fatalf("granted group send: error %q, messages %+v", errText, messages)
	}
}
