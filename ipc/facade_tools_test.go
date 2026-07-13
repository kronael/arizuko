package ipc

// Task #40 / spec 5/16: dashd's tool browser (ipc.ListTools) must show the
// cold-tier facade tools the live agent socket mounts via routd's resreg
// postBuild seam — they vanished from the browser when they left ipc's hot-tier.
// ListTools now derives them from the SAME resreg specs routd uses, so the two
// surfaces are single-sourced and can't drift. The blank import populates the
// registry (a daemon binary does this via dashd/main.go).

import (
	"testing"

	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"

	"github.com/mark3labs/mcp-go/mcp"
)

func findTool(tools []mcp.Tool, name string) *mcp.Tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}

// TestListTools_FacadeToolVisibleWithGrant: a folder whose grants cover
// set_web_route sees the facade tool, with the description + input schema
// single-sourced from resreg/resources (so the browser matches the agent socket).
func TestListTools_FacadeToolVisibleWithGrant(t *testing.T) {
	tools := ListTools("atlas", []string{"set_web_route"})
	got := findTool(tools, "set_web_route")
	if got == nil {
		t.Fatal("set_web_route not in ListTools despite matching grant")
	}

	// Single-source: the browser description IS resreg/resources', the exact same
	// value routd's web_routes_resource.go feeds the agent socket derivation.
	want := resources.WebRoutesMCPDoc[resreg.ActionCreate]
	if got.Description != want {
		t.Errorf("set_web_route description = %q, want single-sourced %q", got.Description, want)
	}

	// Input schema carries the flat agent args (path/access/redirect_to) — NOT the
	// WebRoutesRow columns — because MCPArgs overrides RowType reflection.
	for _, arg := range []string{"path", "access", "redirect_to"} {
		if _, ok := got.InputSchema.Properties[arg]; !ok {
			t.Errorf("set_web_route input schema missing %q property", arg)
		}
	}
	if len(got.InputSchema.Properties) != 3 {
		t.Errorf("set_web_route has %d properties, want 3 (path/access/redirect_to)", len(got.InputSchema.Properties))
	}
	req := map[string]bool{}
	for _, r := range got.InputSchema.Required {
		req[r] = true
	}
	if !req["path"] || !req["access"] {
		t.Errorf("set_web_route required = %v, want path+access required", got.InputSchema.Required)
	}
	if req["redirect_to"] {
		t.Error("set_web_route: redirect_to must be optional, not required")
	}
}

// TestListTools_FacadeToolHiddenWithoutGrant: the visibility filter is honored —
// a folder without a set_web_route grant does not see the facade tool.
func TestListTools_FacadeToolHiddenWithoutGrant(t *testing.T) {
	tools := ListTools("atlas", []string{"send"})
	if got := findTool(tools, "set_web_route"); got != nil {
		t.Fatal("set_web_route visible without a matching grant — visibility filter not honored")
	}
}

// TestListTools_AllFacadeFamiliesVisible: one representative tool from each migrated
// facade resource surfaces when granted, proving the registry walk covers every
// family (routes/web_routes/scheduled_tasks/acl/network_rules + groups' register_group
// — the last 5/16 fold, which needed MCPNames on the groups registration to appear).
func TestListTools_AllFacadeFamiliesVisible(t *testing.T) {
	want := []string{"add_route", "set_web_route", "schedule_task", "add_acl", "network_allow", "register_group"}
	tools := ListTools("atlas", []string{"*"})
	for _, name := range want {
		if findTool(tools, name) == nil {
			t.Errorf("facade tool %q missing from ListTools with wildcard grant", name)
		}
	}
}
