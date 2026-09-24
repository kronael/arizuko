package runed

import (
	"os"
	"testing"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/store"
)

// readSecrets picks the operator anchors (ANTHROPIC_API_KEY /
// CLAUDE_CODE_OAUTH_TOKEN) off the host env so the in-container Claude Code SDK
// can reach the LLM — the floor under mergeSecrets. CONNECTOR secrets still flow
// through the broker at tool-call time; the per-user BYOA override rides in
// turn's payload Secrets and is overlaid by mergeSecrets (see TestMergeSecrets).
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

// mergeSecrets: override (the caller's own env-profile keys, narrowed by
// routd.DB.EnvProfileSecrets) wins over base (operator anchors); anchors with no
// override survive; nil base/override are handled. Both sides carry
// store.EnvProfileKeys only — a capability credential never reaches this merge.
func TestMergeSecrets(t *testing.T) {
	base := map[string]string{
		"ANTHROPIC_API_KEY":       "operator-anchor",
		"CLAUDE_CODE_OAUTH_TOKEN": "oauth",
	}
	override := map[string]string{
		"ANTHROPIC_API_KEY": "user-byoa",
		"OPENAI_API_KEY":    "user-openai",
	}
	got := mergeSecrets(base, override)
	if got["ANTHROPIC_API_KEY"] != "user-byoa" {
		t.Errorf("ANTHROPIC_API_KEY = %q, want user-byoa (override wins)", got["ANTHROPIC_API_KEY"])
	}
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != "oauth" {
		t.Errorf("anchor without override dropped: %q", got["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if got["OPENAI_API_KEY"] != "user-openai" {
		t.Errorf("override-only key missing: %q", got["OPENAI_API_KEY"])
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

// A configured gateway withholds the model credential from BOTH sources — the
// host anchor and the trigger user's own env-profile row — while a `shell:env`
// capability key still passes, and the virtual key still reaches the spawn as
// the guest env (spec 6/23 Fault 1).
func TestSpawnSecrets_GatewayWithholdsModelCredential(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "operator-oauth")
	t.Setenv("ANTHROPIC_API_KEY", "")
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest", ModelGatewayURL: "http://gw:4000"}
	user := map[string]string{"ANTHROPIC_API_KEY": "user-byoa", "GITHUB_TOKEN": "ghp-shell-env"}

	got := spawnSecrets(cfg, user)

	if v, ok := got["CLAUDE_CODE_OAUTH_TOKEN"]; ok {
		t.Errorf("host anchor CLAUDE_CODE_OAUTH_TOKEN=%q reached the spawn with a gateway configured", v)
	}
	if v, ok := got["ANTHROPIC_API_KEY"]; ok {
		t.Errorf("user-scope ANTHROPIC_API_KEY=%q reached the spawn with a gateway configured", v)
	}
	if got["GITHUB_TOKEN"] != "ghp-shell-env" {
		t.Errorf("shell:env key dropped by the gateway filter: %v", got)
	}
	env := turnEnv(cfg, runedv1.RunRequest{ModelGatewayKey: "sk-turn-key"}, 0)
	if env["MODEL_GATEWAY_KEY"] != "sk-turn-key" {
		t.Errorf("virtual key must still reach the guest: %v", env)
	}
}

// No gateway: the spawn receives exactly today's merge — anchor, BYOA override,
// and capability key alike. One code path; the gateway is a value, not a mode.
func TestSpawnSecrets_NoGatewayKeepsBothSources(t *testing.T) {
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "operator-oauth")
	t.Setenv("ANTHROPIC_API_KEY", "operator-anchor")
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	user := map[string]string{"ANTHROPIC_API_KEY": "user-byoa", "GITHUB_TOKEN": "ghp-shell-env"}

	got := spawnSecrets(cfg, user)
	want := mergeSecrets(readSecrets(), user)

	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if got["CLAUDE_CODE_OAUTH_TOKEN"] != "operator-oauth" || got["ANTHROPIC_API_KEY"] != "user-byoa" {
		t.Errorf("unset gateway must change nothing: %v", got)
	}
}

// runed warns once at start when every spawn will carry the model credential in
// container env: no gateway and an anchor set. Neither half alone — a gateway
// withholds the keys (spawnSecrets), and with no anchor there is nothing to
// carry (that failure is eval check 9a, not this warning).
func TestSpawnHoldsModelCredential(t *testing.T) {
	for key := range store.EnvProfileKeys {
		t.Setenv(key, "")
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "operator-oauth")
	if !SpawnHoldsModelCredential(&core.Config{}) {
		t.Error("no gateway with an anchor set: every spawn holds the credential")
	}
	if SpawnHoldsModelCredential(&core.Config{ModelGatewayURL: "http://gw:4000"}) {
		t.Error("a gateway withholds the credential; nothing to warn about")
	}
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "")
	if SpawnHoldsModelCredential(&core.Config{}) {
		t.Error("no anchor set: no credential enters the guest")
	}
}
