package ipc

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"
)

// TestConnector_GitHubPATIntegration smoke-tests the github-mcp connector
// end-to-end when GITHUB_TOKEN is set in the test env (mirroring the
// operator-side path). Skips otherwise. Spec 9/11 M7.
//
// Requires:
//   - GITHUB_TOKEN env (fine-grained PAT, read scope on a public repo)
//   - npx + node on $PATH (the npm-based github-mcp server)
func TestConnector_GitHubPATIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; needs network + github PAT")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Skip("GITHUB_TOKEN unset")
	}
	if _, err := exec.LookPath("npx"); err != nil {
		t.Skip("npx not on PATH")
	}

	spec := &ConnectorSpec{
		Name:    "github",
		Command: []string{"npx", "-y", "@modelcontextprotocol/server-github"},
		Secrets: []string{"GITHUB_TOKEN"},
		EnvTemplate: map[string]string{
			"GITHUB_PERSONAL_ACCESS_TOKEN": "{secret:GITHUB_TOKEN}",
			"PATH":                         os.Getenv("PATH"),
			"HOME":                         os.Getenv("HOME"),
		},
		Scope:       "per_call",
		CallTimeout: 60 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tools, err := DiscoverConnectorTools(ctx, spec)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tools) == 0 {
		t.Fatalf("no tools discovered")
	}
	t.Logf("discovered %d github tools", len(tools))
	for _, tool := range tools {
		if tool.LocalName == "github_get_me" || tool.LocalName == "github_search_repositories" {
			res, err := CallConnectorTool(ctx, tool, map[string]any{}, map[string]string{"GITHUB_TOKEN": token})
			if err != nil {
				t.Fatalf("Call %s: %v", tool.LocalName, err)
			}
			if len(res.Content) == 0 {
				t.Errorf("%s: empty content", tool.LocalName)
			}
			return
		}
	}
	t.Logf("no get_me/search_repositories tool to call; smoke pass")
}
