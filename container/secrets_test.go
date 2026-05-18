package container

import (
	"os"
	"testing"
)

// readSecrets is the only env-injection path post 9/11 M5: it picks
// operator anchors (ANTHROPIC_API_KEY / CLAUDE_CODE_OAUTH_TOKEN) off
// the gated host env so the in-container Claude Code SDK can reach
// the LLM. Folder- and user-scoped secrets never travel via env;
// they flow through the broker at tool-call time.
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
