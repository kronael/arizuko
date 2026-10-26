package runed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
)

// settingsFor renders one turn's settings.json into a temp .claude dir and
// returns the decoded file. cfg carries a staged app dir so the per-spawn
// refresh of the platform-owned files has somewhere to read from.
func settingsFor(t *testing.T, cfg *core.Config, req runedv1.RunRequest, queryTimeout int64) (map[string]any, string) {
	t.Helper()
	d := t.TempDir()
	if cfg.HostAppDir == "" {
		cfg.HostAppDir = t.TempDir()
	}
	seedSettings(d, cfg, req, queryTimeout)
	data, err := os.ReadFile(filepath.Join(d, "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not written: %v", err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatalf("settings.json is not JSON: %v", err)
	}
	return s, d
}

func settingsEnv(t *testing.T, s map[string]any) map[string]any {
	t.Helper()
	env, ok := s["env"].(map[string]any)
	if !ok {
		t.Fatalf("settings env is not a map: %v", s["env"])
	}
	return env
}

// An elevated /root turn: the agent is told it is root, publishes under
// /pub/<folder>/, and sees the instance-wide world "".
func TestSeedSettingsElevated(t *testing.T) {
	cfg := &core.Config{Name: "TestBot", WebHost: "https://example.com"}
	s, _ := settingsFor(t, cfg, runedv1.RunRequest{
		Folder: "testgroup", Channel: "telegram", Elevated: true,
	}, 0)

	env := settingsEnv(t, s)
	for key, want := range map[string]any{
		"ARIZUKO_ASSISTANT_NAME": "TestBot",
		"ARIZUKO_IS_ROOT":        "1",
		"WEB_HOST":               "https://example.com",
		"ARIZUKO_GROUP_FOLDER":   "testgroup",
		"ARIZUKO_GROUP_NAME":     "testgroup",
		"ARIZUKO_WORLD":          "",
		"WEB_PREFIX":             "pub",
	} {
		if env[key] != want {
			t.Errorf("env[%s]=%#v want %#v", key, env[key], want)
		}
	}
	for _, dropped := range []string{"ARIZUKO_TIER", "ARIZUKO_DELEGATE_DEPTH"} {
		if _, ok := env[dropped]; ok {
			t.Errorf("%s must be dropped (5/33-paths-roles decision 10 — no rank, no delegate depth)", dropped)
		}
	}
	servers, ok := s["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers is not a map: %v", s["mcpServers"])
	}
	arz, ok := servers["arizuko"].(map[string]any)
	if !ok {
		t.Fatalf("the arizuko MCP server is missing: %v", servers)
	}
	if arz["command"] != "socat" {
		t.Errorf("mcpServers.arizuko.command=%#v want socat", arz["command"])
	}
}

// A nested folder holding web:publish: not root, and its publishing surface is
// its world's vhost, named by the top segment.
func TestSeedSettingsNestedPublisher(t *testing.T) {
	s, _ := settingsFor(t, &core.Config{Name: "Bot"}, runedv1.RunRequest{
		Folder: "atlas/support", WebPublish: true,
	}, 0)
	env := settingsEnv(t, s)
	for key, want := range map[string]any{
		"ARIZUKO_IS_ROOT":      "",
		"ARIZUKO_WORLD":        "atlas",
		"ARIZUKO_GROUP_PARENT": "atlas",
		"ARIZUKO_GROUP_NAME":   "support",
		"WEB_PREFIX":           "atlas",
	} {
		if env[key] != want {
			t.Errorf("env[%s]=%#v want %#v", key, env[key], want)
		}
	}
}

// A top-level world IS its own world: it publishes via the <world> subdomain,
// not a path prefix, and it has no parent.
func TestSeedSettingsTopLevelWorld(t *testing.T) {
	s, _ := settingsFor(t, &core.Config{Name: "Bot"}, runedv1.RunRequest{
		Folder: "atlas", WebPublish: true,
	}, 0)
	env := settingsEnv(t, s)
	if env["ARIZUKO_WORLD"] != "atlas" || env["WEB_PREFIX"] != "atlas" {
		t.Errorf("world=%#v prefix=%#v want atlas/atlas", env["ARIZUKO_WORLD"], env["WEB_PREFIX"])
	}
	if env["ARIZUKO_GROUP_PARENT"] != "" {
		t.Errorf("parent=%#v want empty", env["ARIZUKO_GROUP_PARENT"])
	}
}

// No web grant, no surface: WEB_PREFIX is empty however deep the folder sits.
func TestSeedSettingsWithoutWebGrantHasNoPrefix(t *testing.T) {
	s, _ := settingsFor(t, &core.Config{Name: "Bot"}, runedv1.RunRequest{
		Folder: "atlas/eng/sre",
	}, 0)
	if got := settingsEnv(t, s)["WEB_PREFIX"]; got != "" {
		t.Errorf("WEB_PREFIX=%#v want empty with no web:publish grant", got)
	}
}

// The agent's own keys and an operator's own settings survive the per-turn
// rewrite: the renderer sets what it owns and leaves the rest.
func TestSeedSettingsPreservesExisting(t *testing.T) {
	cfg := &core.Config{Name: "Bot", HostAppDir: t.TempDir()}
	d := t.TempDir()
	fp := filepath.Join(d, "settings.json")
	existing, _ := json.MarshalIndent(map[string]any{
		"customKey": "preserved",
		"env":       map[string]any{"MY_VAR": "keep"},
	}, "", "  ")
	if err := os.WriteFile(fp, existing, 0o644); err != nil {
		t.Fatal(err)
	}

	seedSettings(d, cfg, runedv1.RunRequest{Folder: "g"}, 0)

	data, _ := os.ReadFile(fp)
	var s map[string]any
	json.Unmarshal(data, &s)
	if s["customKey"] != "preserved" {
		t.Errorf("customKey=%#v — an operator key was overwritten", s["customKey"])
	}
	if got := settingsEnv(t, s)["MY_VAR"]; got != "keep" {
		t.Errorf("env[MY_VAR]=%#v — an existing env var was overwritten", got)
	}
}

// The platform asserts its permission + sandbox posture every turn, overwriting
// a stray agent-added block: isolation is the proxy, the substrate and the gated
// MCP socket, never Claude Code's own permission layer (ant/CLAUDE.md).
func TestSeedSettingsAssertsPosture(t *testing.T) {
	cfg := &core.Config{Name: "Bot", HostAppDir: t.TempDir()}
	d := t.TempDir()
	fp := filepath.Join(d, "settings.json")
	stray, _ := json.MarshalIndent(map[string]any{
		"permissions": map[string]any{"allow": []string{"WebFetch", "WebSearch"}},
		"sandbox":     map[string]any{"enabled": true},
	}, "", "  ")
	if err := os.WriteFile(fp, stray, 0o644); err != nil {
		t.Fatal(err)
	}

	seedSettings(d, cfg, runedv1.RunRequest{Folder: "atlas/support"}, 0)

	data, _ := os.ReadFile(fp)
	var s map[string]any
	json.Unmarshal(data, &s)
	perms, ok := s["permissions"].(map[string]any)
	if !ok || perms["defaultMode"] != "bypassPermissions" {
		t.Errorf("permissions=%v want defaultMode=bypassPermissions", s["permissions"])
	}
	if perms["allow"] != nil {
		t.Errorf("a stray permissions.allow survived: %v", perms["allow"])
	}
	sb, ok := s["sandbox"].(map[string]any)
	if !ok || sb["enabled"] != false {
		t.Errorf("sandbox=%v want enabled=false", s["sandbox"])
	}
}

// routd pins one transcript per chat and resumes it, so Claude Code's default
// 30-day retention must never prune it out from under the sessions table.
func TestSeedSettingsPinsTranscriptRetention(t *testing.T) {
	s, _ := settingsFor(t, &core.Config{Name: "Bot"}, runedv1.RunRequest{Folder: "atlas"}, 0)
	days, ok := s["cleanupPeriodDays"].(float64)
	if !ok {
		t.Fatalf("cleanupPeriodDays missing or not a number: %v", s["cleanupPeriodDays"])
	}
	if days != 3650000 {
		t.Errorf("cleanupPeriodDays=%v want 3650000 (never prune)", days)
	}
}

// The query timeout reaches the SDK subprocess through settings.json as well as
// the process env: the agent aborts and summarizes before the hard kill.
func TestSeedSettingsCarriesTheQueryTimeout(t *testing.T) {
	s, _ := settingsFor(t, &core.Config{Name: "Bot"}, runedv1.RunRequest{Folder: "atlas"}, 1170000)
	if got := settingsEnv(t, s)["ARIZUKO_QUERY_TIMEOUT_MS"]; got != "1170000" {
		t.Errorf("env[ARIZUKO_QUERY_TIMEOUT_MS]=%#v want \"1170000\"", got)
	}
	plain, _ := settingsFor(t, &core.Config{Name: "Bot"}, runedv1.RunRequest{Folder: "atlas"}, 0)
	if _, ok := settingsEnv(t, plain)["ARIZUKO_QUERY_TIMEOUT_MS"]; ok {
		t.Error("no ceiling must leave the key absent, not zero")
	}
}
