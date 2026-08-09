package ipc

import (
	"testing"
)

// ipcAuditRow is one LogIPCAudit call captured by the tests below.
type ipcAuditRow struct {
	folder  string
	sub     string
	tool    string
	outcome string
}

// TestGateJIDDenialIsAudited pins BUGS F70's second half: the messaging tools
// hand-copied granted()'s magnitude check without granted()'s emitAuthzDenied,
// so a denial on the highest-traffic tools left no audit_log row. Both denial
// branches — magnitude (mcp:<tool> not held) and JID containment (chat belongs
// to another folder) — must record.
func TestGateJIDDenialIsAudited(t *testing.T) {
	for _, tc := range []struct {
		name string
		db   StoreFns
	}{
		{
			// Magnitude: the caller holds nothing, so authorizeCall denies.
			name: "magnitude",
			db: StoreFns{
				Authorize: func(_, _, _ string, _ map[string]string) bool { return false },
			},
		},
		{
			// Containment: mcp:send is held, but the chat routes to a sibling
			// folder, so authorizeJID denies.
			name: "containment",
			db: StoreFns{
				Authorize:           func(_, _, _ string, _ map[string]string) bool { return true },
				DefaultFolderForJID: func(string) string { return "world/other" },
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			sock := dir + "/gated.sock"

			var rows []ipcAuditRow
			sent := 0
			db := tc.db
			db.LogIPCAudit = func(folder, sub, tool, _, outcome string) error {
				rows = append(rows, ipcAuditRow{folder, sub, tool, outcome})
				return nil
			}
			gated := GatedFns{SendReply: func(string, string, string) (string, error) { sent++; return "m1", nil }}

			stop, err := ServeMCP(sock, gated, db, "world/a", false, 0, "folder:world/a")
			if err != nil {
				t.Fatalf("ServeMCP: %v", err)
			}
			defer stop()

			_, errText := callTool(t, sock, "reply", map[string]any{
				"chatJid": "telegram:chat/9", "text": "hi",
			})
			if errText == "" {
				t.Fatalf("reply should be denied")
			}
			if sent != 0 {
				t.Fatalf("reply delivered despite denial: %d", sent)
			}
			if len(rows) != 1 {
				t.Fatalf("LogIPCAudit rows = %d, want 1 (%+v)", len(rows), rows)
			}
			got := rows[0]
			if got.tool != "reply" || got.outcome != "authz_denied" {
				t.Errorf("audit row = %+v, want tool=reply outcome=authz_denied", got)
			}
			if got.folder != "world/a" || got.sub != "folder:world/a" {
				t.Errorf("audit row actor/folder = %+v, want folder:world/a on world/a", got)
			}
		})
	}
}

// TestGateJIDAllowsContainedChat guards the other side: the helper must not turn
// a permitted call into a denial, and an allowed call writes no denial row.
func TestGateJIDAllowsContainedChat(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	var rows []ipcAuditRow
	sent := 0
	db := StoreFns{
		Authorize:           func(_, _, _ string, _ map[string]string) bool { return true },
		DefaultFolderForJID: func(string) string { return "world/a/sub" },
		LogIPCAudit: func(folder, sub, tool, _, outcome string) error {
			rows = append(rows, ipcAuditRow{folder, sub, tool, outcome})
			return nil
		},
	}
	gated := GatedFns{SendReply: func(string, string, string) (string, error) { sent++; return "m1", nil }}

	stop, err := ServeMCP(sock, gated, db, "world/a", false, 0, "folder:world/a")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	if _, errText := callTool(t, sock, "reply", map[string]any{
		"chatJid": "telegram:chat/9", "text": "hi",
	}); errText != "" {
		t.Fatalf("reply to a descendant folder should be allowed: %s", errText)
	}
	if sent != 1 {
		t.Fatalf("reply delivered %d times, want 1", sent)
	}
	if len(rows) != 0 {
		t.Fatalf("allowed call wrote denial rows: %+v", rows)
	}
}
