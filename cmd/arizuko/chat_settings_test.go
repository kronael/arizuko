package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/audit"
)

func TestChatThreadRepliesDispatch(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	if err := os.MkdirAll(filepath.Join(base, "arizuko_demo"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := instanceStore(t)
	const jid = "discord:g/chat"
	if err := s.SetChatIsGroup(jid, true); err != nil {
		t.Fatal(err)
	}
	args, err := instanceArgs([]string{"-i", "demo", "thread-replies", jid, "off"})
	if err != nil {
		t.Fatal(err)
	}
	cmdChat(args)
	if got, err := s.ChatThreadReplies(jid); err != nil || got {
		t.Fatalf("thread replies = %v, err = %v; want false", got, err)
	}
}

func TestChatThreadRepliesUsage(t *testing.T) {
	if os.Getenv("ARIZUKO_PKG_ARGS") == "chat-usage" {
		args, err := instanceArgs([]string{"-i", "demo", "thread-replies"})
		if err != nil {
			t.Fatal(err)
		}
		cmdChat(args)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestChatThreadRepliesUsage$")
	cmd.Env = append(os.Environ(), "ARIZUKO_PKG_ARGS=chat-usage")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "usage: arizuko chat [-i instance] thread-replies <jid> [on|off|default]") {
		t.Fatalf("usage error = %v, output = %s", err, out)
	}
}

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
