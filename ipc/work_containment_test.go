package ipc

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func TestWorkSymlinkContainment(t *testing.T) {
	for _, tool := range []string{"get_work", "set_work"} {
		t.Run(tool, func(t *testing.T) {
			root := t.TempDir()
			own := filepath.Join(root, "alpha")
			sibling := filepath.Join(root, "beta")
			for _, dir := range []string{own, sibling} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(sibling, "private.txt")
			if err := os.WriteFile(target, []byte("secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			name := "work.md"
			if tool == "set_work" {
				name += ".tmp"
			}
			if err := os.Symlink("../beta/private.txt", filepath.Join(own, name)); err != nil {
				t.Fatal(err)
			}
			srv := buildMCPServer(RoutdFns{GroupsDir: root}, StoreFns{}, "alpha", false, "folder:alpha")
			var req mcp.CallToolRequest
			req.Params.Name = tool
			req.Params.Arguments = map[string]any{"content": "changed"}
			result, err := srv.GetTool(tool).Handler(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(target)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), "secret") || string(content) != "secret" {
				t.Errorf("result=%s sibling=%q", encoded, content)
			}
		})
	}
}
