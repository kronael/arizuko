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
	audit.Init(d.Obj(), "test")
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

	n := 0
	var denial audit.Row
	for _, r := range auditRowsFor(t, d, "mcp.tool.invoke") {
		if r.Surface == audit.SurfaceMCP {
			n++
		}
		if r.Resource == "mcp/reply" {
			denial = r
		}
	}
	if n != 2 {
		t.Fatalf("mcp.tool.invoke rows = %d, want 2", n)
	}
	if denial.Outcome != audit.OutcomeDenied || denial.Folder != "atlas/support" {
		t.Errorf("denial row outcome=%q folder=%q, want %q on atlas/support",
			denial.Outcome, denial.Folder, audit.OutcomeDenied)
	}
}
