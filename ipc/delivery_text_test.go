package ipc

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
)

// Every text-bearing delivery verb refuses before adapter calls or message
// writes. A file caption alone may be empty, and an ordinary reply is unchanged.
func TestDeliveryTextRefusedBeforeSideEffects(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "demo"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo", "file.txt"), []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	calls, writes := 0, 0
	routd := RoutdFns{
		GroupsDir:    dir,
		SendMessage:  func(_, _, _ string) (string, string, error) { calls++; return "sent", "", nil },
		SendReply:    func(_, _, _, _ string) (string, string, error) { calls++; return "sent", "", nil },
		SendDocument: func(_, _, _, _, _, _, _ string) (string, string, error) { calls++; return "sent", "", nil },
		SendVoice:    func(_, _, _, _, _ string) (string, string, error) { calls++; return "sent", "", nil },
		Post:         func(_, _ string, _ []string) (string, error) { calls++; return "sent", nil },
		Edit:         func(_, _, _ string) error { calls++; return nil },
	}
	sock := filepath.Join(dir, "gated.sock")
	stop, err := ServeMCP(sock, routd, StoreFns{PutOutbound: func(core.Message, string) error { writes++; return nil }}, "demo", true, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	for _, tool := range []struct{ name, arg string }{{"reply", "text"}, {"send", "text"}, {"send_file", "caption"}, {"send_voice", "text"}, {"post", "content"}, {"edit", "content"}} {
		for _, text := range []string{"  ", "$WEB_HOST/pub/result", "/home/node/file.txt", "/var/lib/www/result"} {
			if tool.name == "send_file" && text == "  " {
				continue
			}
			args := map[string]any{"chatJid": "tg:1", "filepath": "/home/node/file.txt", "targetId": "p1", tool.arg: text}
			_, msg := callTool(t, sock, tool.name, args)
			if msg == "" || calls != 0 || writes != 0 {
				t.Fatalf("%s %q msg=%q calls=%d writes=%d", tool.name, text, msg, calls, writes)
			}
		}
	}
	if _, msg := callTool(t, sock, "send_file", map[string]any{"chatJid": "tg:1", "filepath": "/home/node/file.txt", "caption": ""}); msg != "" {
		t.Fatal(msg)
	}
	if calls != 1 || writes != 1 {
		t.Fatalf("empty caption calls=%d writes=%d", calls, writes)
	}
	if _, msg := callTool(t, sock, "reply", map[string]any{"chatJid": "tg:1", "text": "the answer"}); msg != "" {
		t.Fatal(msg)
	}
	if calls != 2 || writes != 2 {
		t.Fatalf("ordinary reply calls=%d writes=%d", calls, writes)
	}
}
