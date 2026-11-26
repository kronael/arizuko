package routd

import (
	"slices"
	"testing"

	"github.com/kronael/arizuko/anteval/pkg/spec"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
)

// The runner grants each `mcp:<tool>` a case requires. A requirement that
// names no registered tool grants nothing the agent can call, and the case
// fails as an agent failure: `web-route` required `mcp:web_route_set` while the
// tool is `set_web_route`.
func TestCaseRequiresTheRegisteredTool(t *testing.T) {
	cases, err := spec.Load("../anteval/cases")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"chat-entrypoint":   "mcp:" + routeTokensMCPNames[routeTokensActionIssueChat],
		"web-route":         "mcp:" + resources.WebRoutesMCPNames[resreg.ActionCreate],
		"web-route-private": "mcp:" + resources.WebRoutesMCPNames[resreg.ActionCreate],
	}
	for _, c := range cases {
		tool, ok := want[c.ID]
		if !ok {
			continue
		}
		if !slices.Contains(c.Requires, tool) {
			t.Errorf("%s requires %v, want %q", c.ID, c.Requires, tool)
		}
		delete(want, c.ID)
	}
	for id := range want {
		t.Errorf("%s case missing", id)
	}
}
