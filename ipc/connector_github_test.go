package ipc

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestConnector_GitHubPATIntegration drives the REAL GitHub remote MCP server
// through the connector when GITHUB_TOKEN is set, mirroring the operator path.
// Skips otherwise. Spec 5/13 shape 2.
//
// It needs no local runtime: the endpoint is hosted, which is the whole reason
// the connector moved off stdio (BUGS L132). A PAT is accepted as the bearer;
// the same slot takes a surrogate-OAuth access token (spec 5/15).
func TestConnector_GitHubPATIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test; needs network + github PAT")
	}
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" {
		t.Skip("GITHUB_TOKEN unset")
	}

	spec := &ConnectorSpec{
		Name:        "github",
		URL:         "https://api.githubcopilot.com/mcp/",
		Secrets:     []string{"GITHUB_TOKEN"},
		Headers:     map[string]string{"Authorization": "Bearer {secret:GITHUB_TOKEN}"},
		CallTimeout: 60 * time.Second,
	}
	secrets := map[string]string{"GITHUB_TOKEN": token}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	tools, err := DiscoverConnectorTools(ctx, spec, secrets)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if len(tools) == 0 {
		t.Fatalf("no tools discovered")
	}
	t.Logf("discovered %d github tools", len(tools))
	for _, tool := range tools {
		if tool.LocalName != "github_get_me" {
			continue
		}
		res, err := CallConnectorTool(ctx, tool, map[string]any{}, secrets)
		if err != nil {
			t.Fatalf("Call %s: %v", tool.LocalName, err)
		}
		if len(res.Content) == 0 {
			t.Errorf("%s: empty content", tool.LocalName)
		}
		return
	}
	t.Logf("no get_me tool to call; discovery smoke pass")
}
