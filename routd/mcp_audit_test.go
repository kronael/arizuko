package routd

import (
	"testing"

	"github.com/kronael/arizuko/audit"
)

// TestBuildStoreFnsLogsIPCAudit pins BUGS F70's first half: StoreFns.LogIPCAudit
// was left nil behind a comment claiming routd writes no SQLite audit_log, so
// every agent MCP tool call the hand-authored tools record was dropped on the
// floor. routd owns audit_log (migration 0016) and cmd/routd runs audit.Init on
// it, so the sink must be wired and its rows must land in routd.db.
func TestBuildStoreFnsLogsIPCAudit(t *testing.T) {
	d, err := OpenMem()
	if err != nil {
		t.Fatalf("OpenMem: %v", err)
	}
	defer d.Close()
	audit.Init(d.SQL(), "test")
	t.Cleanup(func() { audit.Init(nil, "") })

	s := NewServer(d, nil, nil, nil, 0, "")
	fns := s.buildStoreFns(turnMCP{folder: "atlas/support", turnID: "t1"})
	if fns.LogIPCAudit == nil {
		t.Fatal("StoreFns.LogIPCAudit is nil: MCP tool calls are unauditable")
	}
	if err := fns.LogIPCAudit("atlas/support", "folder:atlas/support", "set_group_open",
		`{"open":true}`, "ok"); err != nil {
		t.Fatalf("LogIPCAudit: %v", err)
	}
	if err := fns.LogIPCAudit("atlas/support", "folder:atlas/support", "reply",
		"{}", "authz_denied"); err != nil {
		t.Fatalf("LogIPCAudit denied: %v", err)
	}

	var n int
	if err := d.SQL().QueryRow(
		`SELECT COUNT(*) FROM audit_log WHERE action='mcp.tool.invoke' AND surface=?`,
		audit.SurfaceMCP).Scan(&n); err != nil {
		t.Fatalf("read audit_log: %v", err)
	}
	if n != 2 {
		t.Fatalf("mcp.tool.invoke rows = %d, want 2", n)
	}

	var outcome, resource, folder string
	if err := d.SQL().QueryRow(
		`SELECT outcome, resource, folder FROM audit_log WHERE resource='mcp/reply'`).
		Scan(&outcome, &resource, &folder); err != nil {
		t.Fatalf("read denial row: %v", err)
	}
	if outcome != audit.OutcomeDenied || folder != "atlas/support" {
		t.Errorf("denial row outcome=%q folder=%q, want %q on atlas/support", outcome, folder, audit.OutcomeDenied)
	}
}
