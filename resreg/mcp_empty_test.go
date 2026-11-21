package resreg

import (
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestMCPArgsPreserveEmptyStrings(t *testing.T) {
	var req mcp.CallToolRequest
	req.Params.Arguments = map[string]any{"gated_by": "", "invalid": 1}
	got := decodeMCPArgs(req, []MCPArg{
		{Name: "gated_by", Type: "string"},
		{Name: "omitted", Type: "string"},
		{Name: "invalid", Type: "string"},
	})
	if v, present := got["gated_by"]; !present || v != "" {
		t.Fatalf("explicit empty string = %v, present = %v", v, present)
	}
	if len(got) != 1 {
		t.Fatalf("omitted or invalid argument copied: %v", got)
	}
}
