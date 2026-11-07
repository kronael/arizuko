package ipc

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

func TestSendGroupAndPlatform(t *testing.T) {
	sock := t.TempDir() + "/send.sock"
	var outbound []outboundRow
	var messages []core.Message
	var queued []string
	db := captureStore(&outbound)
	db.Authorize = func(target, action string, _ map[string]string) bool {
		return action == "mcp:send" && target != "world/denied"
	}
	db.DefaultFolderForJID = func(string) string { return "world/a" }
	db.PutMessage = func(m core.Message) error { messages = append(messages, m); return nil }
	sent := 0
	routd := RoutdFns{
		SendMessage:         func(string, string, string) (string, string, error) { sent++; return "m1", "", nil },
		EnqueueMessageCheck: func(target string) { queued = append(queued, target) },
	}
	stop, err := ServeMCP(sock, routd, db, "world/a", false, 0, "folder:world/a")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()

	result, errText := callTool(t, sock, "send", map[string]any{"chatJid": "world/peer", "text": "hi"})
	if errText != "" || len(messages) != 1 || messages[0].ChatJID != "world/peer" || messages[0].Content != "hi" || messages[0].ForwardedFrom != "world/a" || result["id"] != messages[0].ID || len(queued) != 1 || queued[0] != "world/peer" {
		t.Fatalf("group send: result %v error %q messages %+v queued %v", result, errText, messages, queued)
	}
	_, errText = callTool(t, sock, "send", map[string]any{"chatJid": "world/denied", "text": "hi"})
	if !strings.Contains(errText, "granted scope") || len(messages) != 1 || len(queued) != 1 {
		t.Fatalf("denied send: error %q messages %+v queued %v", errText, messages, queued)
	}
	if _, errText := callTool(t, sock, "send", map[string]any{"chatJid": "telegram:user/137865889", "text": "hi"}); errText != "" {
		t.Fatalf("platform send: %s", errText)
	}
	if sent != 1 || len(outbound) != 1 || outbound[0].Status != core.MessageStatusSent {
		t.Fatalf("platform send: delivered %d, rows %+v", sent, outbound)
	}
}

// TestSendIssuesDestination: `send` to `#issues` hands the text to routd's
// destination list, needs no grant to any chat, and never reaches SendMessage.
func TestSendIssuesDestination(t *testing.T) {
	sock := t.TempDir() + "/issues.sock"
	var rows []outboundRow
	db := captureStore(&rows)
	db.Authorize = func(_, _ string, _ map[string]string) bool { return true }
	db.DefaultFolderForJID = func(string) string { return "" }
	sent, queued, dests := 0, []string{}, 1
	routd := RoutdFns{
		SendMessage: func(string, string, string) (string, string, error) {
			sent++
			return "m1", "", nil
		},
		QueueIssueNotice: func(text string) (int, error) {
			queued = append(queued, text)
			return dests, nil
		},
	}
	stop, err := ServeMCP(sock, routd, db, "world/a", false, 0, "folder:world/a")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	text, errText := callTool(t, sock, "send", map[string]any{"chatJid": "#issues", "text": "[a] b"})
	if errText != "" || text["ok"] != true || text["delivered"] != float64(1) {
		t.Fatalf("send #issues: text %q err %q", text, errText)
	}
	if len(queued) != 1 || queued[0] != "[a] b" || sent != 0 || len(rows) != 0 {
		t.Fatalf("queued %v sent %d rows %+v", queued, sent, rows)
	}

	dests = 0
	text, errText = callTool(t, sock, "send", map[string]any{"chatJid": "#issues", "text": "[a] b"})
	if errText != "" || text["ok"] != false || text["reason"] != "no #issues destination set" {
		t.Fatalf("no destination: text %q err %q", text, errText)
	}
}
