package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/audit"
)

func TestChatThreadRepliesCLI(t *testing.T) {
	s := routdStore(t)
	const jid = "discord:g/chat"
	if err := s.SetChatIsGroup(jid, true); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		args []string
		want bool
	}{
		{[]string{jid}, true}, {[]string{jid, "off"}, false}, {[]string{jid, "on"}, true}, {[]string{jid, "default"}, true},
	} {
		var out bytes.Buffer
		if err := runChatThreadReplies(s.AsCLI("alice"), "alice", test.args, &out); err != nil {
			t.Fatal(err)
		}
		got, err := s.ChatThreadReplies(jid)
		if err != nil || got != test.want || !strings.Contains(out.String(), map[bool]string{true: "true", false: "false"}[test.want]) {
			t.Fatalf("args=%v value=%v output=%q err=%v", test.args, got, out.String(), err)
		}
	}
	rows, err := audit.Query(t.Context(), s.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, row := range rows {
		if row.Action == "chats:update" {
			n++
			if row.Actor != "cli:alice" || row.ActorSub != "alice" || row.Surface != "cli" {
				t.Fatalf("audit = %+v", row)
			}
		}
	}
	if n != 3 {
		t.Fatalf("audit rows = %d, want 3", n)
	}
	if err := runChatThreadReplies(s, "alice", []string{jid, "bad"}, &bytes.Buffer{}); err == nil {
		t.Fatal("invalid value accepted")
	}
}

func TestChatInteractiveDispatch(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	sock := filepath.Join(base, "arizuko_demo", "ipc", "team", "gated.sock")
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	capture := filepath.Join(bin, "called")
	for name, script := range map[string]string{"claude": "#!/bin/sh\nprintf '%s' \"$1\" > \"$CHAT_TEST_CAPTURE\"\n", "socat": "#!/bin/sh\nexit 0\n"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CHAT_TEST_CAPTURE", capture)
	cmdChat([]string{"demo", "--folder", "team"})
	got, err := os.ReadFile(capture)
	if err != nil || string(got) != "--mcp-config" {
		t.Fatalf("interactive dispatch = %q, %v", got, err)
	}
}
