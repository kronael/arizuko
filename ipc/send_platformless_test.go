package ipc

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// TestSendRefusesJIDWithoutPlatform: `send` to `user/137865889` reached the
// deliverer, which found no channel, and the failure path wrote a pending row
// that made routd store a chat for it. The tool refuses at the door: no
// delivery attempt, no row.
func TestSendRefusesJIDWithoutPlatform(t *testing.T) {
	sock := t.TempDir() + "/send.sock"
	var rows []outboundRow
	db := captureStore(&rows)
	db.Authorize = func(_, _ string, _ map[string]string) bool { return true }
	db.DefaultFolderForJID = func(string) string { return "world/a" }
	sent := 0
	routd := RoutdFns{SendMessage: func(string, string, string) (string, string, error) {
		sent++
		return "m1", "", nil
	}}
	stop, err := ServeMCP(sock, routd, db, "world/a", false, 0, "folder:world/a")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	_, errText := callTool(t, sock, "send", map[string]any{"chatJid": "user/137865889", "text": "hi"})
	if !strings.Contains(errText, "no platform") {
		t.Fatalf("send to a platformless jid: error %q, want a no-platform refusal", errText)
	}
	if sent != 0 || len(rows) != 0 {
		t.Fatalf("refused send still delivered %d, wrote %d rows: %+v", sent, len(rows), rows)
	}

	if _, errText := callTool(t, sock, "send", map[string]any{"chatJid": "telegram:user/137865889", "text": "hi"}); errText != "" {
		t.Fatalf("send to a platform jid: %s", errText)
	}
	if sent != 1 || len(rows) != 1 || rows[0].Status != core.MessageStatusSent {
		t.Fatalf("control: delivered %d, rows %+v", sent, rows)
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
