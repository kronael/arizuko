package resources

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func TestACLPrincipalMCPDescription(t *testing.T) {
	srv := mcpserver.NewMCPServer("test", "1")
	resreg.MCPTools(srv, *resreg.Lookup(ACLName), nil, nil)
	want := auth.FolderPrincipal("<path>")
	found := 0
	for _, tool := range srv.ListTools() {
		if tool.Tool.Name != ACLMCPNames[resreg.Action("add")] && tool.Tool.Name != ACLMCPNames[resreg.Action("remove")] {
			continue
		}
		found++
		data, err := json.Marshal(tool.Tool.InputSchema.Properties["principal"])
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Description string `json:"description"`
		}
		if err := json.Unmarshal(data, &schema); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(schema.Description, want) {
			t.Errorf("%s principal schema = %s, want %q", tool.Tool.Name, data, want)
		}
	}
	if found != 2 {
		t.Fatalf("found %d ACL tools, want 2", found)
	}
}
