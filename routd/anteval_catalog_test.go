package routd

import (
	"slices"
	"testing"

	"github.com/kronael/arizuko/anteval/pkg/spec"
)

func TestChatEntrypointRequiresRegisteredTool(t *testing.T) {
	cases, err := spec.Load("../anteval/cases")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		if c.ID == "chat-entrypoint" {
			want := "mcp:" + routeTokensMCPNames[routeTokensActionIssueChat]
			if !slices.Contains(c.Requires, want) {
				t.Fatalf("chat-entrypoint requires %v, want %q", c.Requires, want)
			}
			return
		}
	}
	t.Fatal("chat-entrypoint case missing")
}
