package ipc

import (
	"testing"
)

// network_allow → network_list round-trips through StoreFns, and a tier-2
// caller is denied egress management over its own / a sibling folder.
func TestServeMCP_NetworkAllowList(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	rules := map[string][]string{} // folder → explicit targets
	db := StoreFns{
		AddNetworkRule: func(folder, target, _ string) error {
			rules[folder] = append(rules[folder], target)
			return nil
		},
		RemoveNetworkRule: func(folder, target string) error {
			kept := rules[folder][:0]
			for _, t := range rules[folder] {
				if t != target {
					kept = append(kept, t)
				}
			}
			rules[folder] = kept
			return nil
		},
		ListNetworkRules: func(folder string) ([]NetworkRule, error) {
			var out []NetworkRule
			for _, t := range rules[folder] {
				out = append(out, NetworkRule{Folder: folder, Target: t})
			}
			return out, nil
		},
		ResolveAllowlist: func(folder string) ([]string, error) {
			// Stand-in: base + the folder's own rules.
			out := []string{"anthropic.com"}
			out = append(out, rules[folder]...)
			return out, nil
		},
	}

	// Root (tier 0) over a descendant folder.
	stop, err := ServeMCP(sock, GatedFns{}, db, "world", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	if _, errText := callTool(t, sock, "network_allow", map[string]any{
		"folder": "world/a", "host": "example.com",
	}); errText != "" {
		t.Fatalf("network_allow (root → descendant): %s", errText)
	}

	out, errText := callTool(t, sock, "network_list", map[string]any{"folder": "world/a"})
	if errText != "" {
		t.Fatalf("network_list: %s", errText)
	}
	own, _ := out["own"].([]any)
	if len(own) != 1 {
		t.Fatalf("own rules = %v, want 1", out["own"])
	}
	resolved, _ := out["resolved"].([]any)
	foundHost, foundBase := false, false
	for _, r := range resolved {
		switch r {
		case "example.com":
			foundHost = true
		case "anthropic.com":
			foundBase = true
		}
	}
	if !foundHost || !foundBase {
		t.Fatalf("resolved = %v, want base + example.com", out["resolved"])
	}

	// network_deny drops the rule.
	if _, errText := callTool(t, sock, "network_deny", map[string]any{
		"folder": "world/a", "host": "example.com",
	}); errText != "" {
		t.Fatalf("network_deny: %s", errText)
	}
	if got := rules["world/a"]; len(got) != 0 {
		t.Fatalf("rules after deny = %v, want empty", got)
	}
}

// A tier-2 caller (folder world/a/b) must not manage egress — for its own
// folder or a sibling. The StoreFns write must never be reached.
func TestServeMCP_NetworkAllowDeniedTier2(t *testing.T) {
	dir := t.TempDir()
	sock := dir + "/gated.sock"

	called := 0
	db := StoreFns{
		AddNetworkRule:    func(_, _, _ string) error { called++; return nil },
		ResolveAllowlist:  func(string) ([]string, error) { return nil, nil },
		ListNetworkRules:  func(string) ([]NetworkRule, error) { return nil, nil },
		RemoveNetworkRule: func(_, _ string) error { return nil },
	}
	stop, err := ServeMCP(sock, GatedFns{}, db, "world/a/b", []string{"*"}, 0, "")
	if err != nil {
		t.Fatalf("ServeMCP: %v", err)
	}
	defer stop()

	// Own folder — still denied (tier 2 cannot manage egress).
	if _, errText := callTool(t, sock, "network_allow", map[string]any{
		"folder": "world/a/b", "host": "example.com",
	}); errText == "" {
		t.Fatal("expected tier-2 network_allow (own folder) to be denied")
	}
	// Escaping/sibling folder — denied.
	if _, errText := callTool(t, sock, "network_allow", map[string]any{
		"folder": "world/x", "host": "example.com",
	}); errText == "" {
		t.Fatal("expected tier-2 network_allow (sibling) to be denied")
	}
	if called != 0 {
		t.Fatalf("AddNetworkRule must not run on denial; got %d calls", called)
	}
}
