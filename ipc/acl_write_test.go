package ipc

import (
	"testing"
)

// add_acl/remove_acl are the MCP write-face of REST POST/DELETE /v1/acl. A
// tier-0 (operator) caller records the GrantACL/RevokeACL call with
// action/effect defaults left to the underlying writer (passed through as "").
func TestServeMCP_ACLWrite_Tier0(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	type call struct{ principal, scope, action, effect string }
	var grants, revokes []call
	gated := GatedFns{
		GrantACL: func(p, sc, a, e string) error {
			grants = append(grants, call{p, sc, a, e})
			return nil
		},
		RevokeACL: func(p, sc, a, e string) error {
			revokes = append(revokes, call{p, sc, a, e})
			return nil
		},
	}
	// folder "root" → tier 0 (operator): any scope allowed, including "**".
	stop, err := ServeMCP(sock, gated, StoreFns{}, "root", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	if _, errText := callTool(t, sock, "add_acl", map[string]any{
		"principal": "github:7", "scope": "main/eng",
	}); errText != "" {
		t.Fatalf("add_acl (tier0): %s", errText)
	}
	if len(grants) != 1 || grants[0] != (call{"github:7", "main/eng", "", ""}) {
		t.Fatalf("GrantACL calls = %+v, want one passthrough call", grants)
	}

	// Operator role "**" is allowed for tier 0.
	if _, errText := callTool(t, sock, "add_acl", map[string]any{
		"principal": "github:9", "scope": "**",
	}); errText != "" {
		t.Fatalf("add_acl ** (tier0): %s", errText)
	}

	if _, errText := callTool(t, sock, "remove_acl", map[string]any{
		"principal": "github:7", "scope": "main/eng",
	}); errText != "" {
		t.Fatalf("remove_acl (tier0): %s", errText)
	}
	if len(revokes) != 1 || revokes[0] != (call{"github:7", "main/eng", "", ""}) {
		t.Fatalf("RevokeACL calls = %+v, want one passthrough call", revokes)
	}
}

// A tier-1 caller may grant inside its own world but not cross-world or the
// operator role "**" — authzStructural denies before the writer runs.
func TestServeMCP_ACLWrite_Tier1Scoped(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	called := 0
	gated := GatedFns{
		GrantACL:  func(_, _, _, _ string) error { called++; return nil },
		RevokeACL: func(_, _, _, _ string) error { called++; return nil },
	}
	// folder "world/a" → tier 1, world "world".
	stop, err := ServeMCP(sock, gated, StoreFns{}, "world/a", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	// In-world: allowed, writer runs.
	if _, errText := callTool(t, sock, "add_acl", map[string]any{
		"principal": "u1", "scope": "world/x",
	}); errText != "" {
		t.Fatalf("add_acl in-world (tier1): %s", errText)
	}
	if called != 1 {
		t.Fatalf("in-world add_acl should reach writer; called=%d", called)
	}

	// Cross-world: denied, writer must not run.
	if _, errText := callTool(t, sock, "add_acl", map[string]any{
		"principal": "u1", "scope": "other/x",
	}); errText == "" {
		t.Fatal("expected tier-1 cross-world add_acl to be denied")
	}
	// Operator role "**": denied for tier 1.
	if _, errText := callTool(t, sock, "add_acl", map[string]any{
		"principal": "u1", "scope": "**",
	}); errText == "" {
		t.Fatal("expected tier-1 add_acl ** (operator) to be denied")
	}
	// remove_acl symmetric: cross-world denied.
	if _, errText := callTool(t, sock, "remove_acl", map[string]any{
		"principal": "u1", "scope": "other/x",
	}); errText == "" {
		t.Fatal("expected tier-1 cross-world remove_acl to be denied")
	}
	if called != 1 {
		t.Fatalf("denied calls must not reach writer; called=%d, want 1", called)
	}
}
