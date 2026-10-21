package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"
)

// callToolText is callTool's sibling for verbs that answer with plain text
// rather than a JSON payload — `like` and the other social verbs return "ok",
// which callTool's json.Unmarshal rejects.
func callToolText(t *testing.T, sock, name string, args map[string]any) string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	req := map[string]any{
		"jsonrpc": "2.0", "id": 1, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args},
	}
	b, _ := json.Marshal(req)
	c.Write(append(b, '\n'))
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var parsed struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if err := json.Unmarshal(resp, &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", resp, err)
	}
	if len(parsed.Result.Content) == 0 {
		t.Fatalf("no content: %s", resp)
	}
	return parsed.Result.Content[0].Text
}

// TestSocialVerbIsAudited pins the trace a social mutation verb leaves. These
// verbs deliberately write no `messages` row — they mutate existing content
// rather than authoring new content (see regSocial's `record` field) — so the
// audit row is their ONLY record. Without it an agent could like, edit or
// delete a message and nothing anywhere recorded that it happened.
func TestSocialVerbIsAudited(t *testing.T) {
	for _, tc := range []struct {
		name    string
		likeErr error
		want    string
	}{
		{name: "ok", want: "ok"},
		{name: "failure", likeErr: errors.New("boom"), want: "error: boom"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sock := dir + "/social.sock"

			var rows []ipcAuditRow
			db := StoreFns{
				Authorize:           func(_, _ string, _ map[string]string) bool { return true },
				DefaultFolderForJID: func(string) string { return "world/a" },
				LogIPCAudit: func(folder, sub, tool, _, outcome string) error {
					rows = append(rows, ipcAuditRow{folder, sub, tool, outcome})
					return nil
				},
			}
			routd := RoutdFns{
				Like: func(_, _, _ string) error { return tc.likeErr },
			}

			stop, err := ServeMCP(sock, routd, db, "world/a", false, 0, "folder:world/a")
			if err != nil {
				t.Fatalf("ServeMCP: %v", err)
			}
			defer stop()

			callToolText(t, sock, "like", map[string]any{
				"chatJid": "telegram:chat/9", "targetId": "m1", "reaction": "👍",
			})

			var got []ipcAuditRow
			for _, r := range rows {
				if r.tool == "like" {
					got = append(got, r)
				}
			}
			if len(got) != 1 {
				t.Fatalf("like audit rows = %d, want 1 (%+v)", len(got), rows)
			}
			if got[0].outcome != tc.want {
				t.Errorf("outcome = %q, want %q", got[0].outcome, tc.want)
			}
			if got[0].folder != "world/a" {
				t.Errorf("folder = %q, want world/a", got[0].folder)
			}
		})
	}
}

// TestAuditParamsCapsLongValues keeps one oversized `content` from bloating
// every audit_log row.
func TestAuditParamsCapsLongValues(t *testing.T) {
	long := ""
	for i := 0; i < 500; i++ {
		long += "x"
	}
	out := auditParams(map[string]string{"chatJid": "telegram:chat/9", "content": long})
	if out["chatJid"] != "telegram:chat/9" {
		t.Errorf("short value altered: %v", out["chatJid"])
	}
	got, _ := out["content"].(string)
	if len([]rune(got)) != 201 {
		t.Errorf("capped length = %d runes, want 201 (200 + ellipsis)", len([]rune(got)))
	}
}
