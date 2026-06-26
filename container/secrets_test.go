package container

import (
	"os"
	"testing"
)

// readSecrets picks the operator anchors (ANTHROPIC_API_KEY /
// CLAUDE_CODE_OAUTH_TOKEN) off the host env so the in-container Claude Code SDK
// can reach the LLM — the floor under mergeSecrets. CONNECTOR secrets still flow
// through the broker at tool-call time; the per-user BYOA override rides in
// container.Input.Secrets and is overlaid by mergeSecrets (see TestMergeSecrets).
func TestReadSecrets_PicksOperatorAnchors(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "anth-xxx")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "oauth-yyy")

	got := readSecrets()

	if got["ANTHROPIC_API_KEY"] != "anth-xxx" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want anth-xxx", got["ANTHROPIC_API_KEY"])
	}
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth-yyy" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q, want oauth-yyy", got["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if len(got) != 2 {
		t.Errorf("unexpected extra keys: %v", got)
	}
}

func TestReadSecrets_OmitsUnsetVars(t *testing.T) {
	os.Unsetenv("ANTHROPIC_API_KEY")
	os.Unsetenv("CLAUDE_CODE_OAUTH_TOKEN")

	got := readSecrets()

	if got != nil {
		t.Errorf("expected nil when no anchors set, got %v", got)
	}
}

// mergeSecrets: override (routd-resolved BYOA) wins over base (operator anchors);
// anchors with no override survive; nil base/override are handled.
func TestMergeSecrets(t *testing.T) {
	base := map[string]string{
		"ANTHROPIC_API_KEY":       "operator-anchor",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth",
	}
	override := map[string]string{
		"ANTHROPIC_API_KEY": "user-byoa",
		"EXTRA_KEY":         "extra",
	}
	got := mergeSecrets(base, override)
	if got["ANTHROPIC_API_KEY"] != "user-byoa" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want user-byoa (override wins)", got["ANTHROPIC_API_KEY"])
	}
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth" {
		t.Errorf("anchor without override dropped: %q", got["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if got["EXTRA_KEY"] != "extra" {
		t.Errorf("override-only key missing: %q", got["EXTRA_KEY"])
	}
}

func TestMergeSecrets_NilBase(t *testing.T) {
	if got := mergeSecrets(nil, map[string]string{"K": "v"}); got["K"] != "v" {
		t.Errorf("nil-base merge = %v, want K=v", got)
	}
}

func TestMergeSecrets_NilOverride(t *testing.T) {
	base := map[string]string{"ANTHROPIC_API_KEY": "anchor"}
	if got := mergeSecrets(base, nil); got["ANTHROPIC_API_KEY"] != "anchor" {
		t.Errorf("nil-override merge = %v, want anchor preserved", got)
	}
}
