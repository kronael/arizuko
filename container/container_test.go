package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
)

func TestSanitizeFolder(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"mygroup", "mygroup"},
		{"my/sub/group", "my-sub-group"},
		{"hello world!", "hello-world"},
		{"---leading", "leading"},
		{"trailing---", "trailing"},
		{"a/b@c#d$e%f", "a-b-c-d-e-f"},
		{"", ""},
		{strings.Repeat("x", 50), strings.Repeat("x", 40)},
		{"abc---def", "abc---def"},
		{"-middle-", "middle"},
	}
	for _, tc := range cases {
		got := SanitizeFolder(tc.in)
		if got != tc.want {
			t.Errorf("SanitizeFolder(%q) = %q, want %q",
				tc.in, got, tc.want)
		}
	}
}

func TestStopContainerArgs(t *testing.T) {
	got := StopContainerArgs("arizuko-test-123")
	if got[0] != "stop" || got[1] != "arizuko-test-123" {
		t.Errorf("got %v", got)
	}
}

func TestHp(t *testing.T) {
	cases := []struct {
		name  string
		cfg   core.Config
		local string
		want  string
	}{
		{
			"no host root",
			core.Config{ProjectRoot: "/srv/data/inst"},
			"/srv/data/inst/groups/g/.claude",
			"/srv/data/inst/groups/g/.claude",
		},
		{
			"with host root",
			core.Config{
				HostProjectRoot: "/host/inst",
				ProjectRoot:     "/srv/data/inst",
			},
			"/srv/data/inst/groups/g/.claude",
			"/host/inst/groups/g/.claude",
		},
		{
			"path outside project",
			core.Config{
				HostProjectRoot: "/host/inst",
				ProjectRoot:     "/srv/data/inst",
			},
			"/other/path",
			"/other/path",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := hp(&tc.cfg, tc.local)
			if got != tc.want {
				t.Errorf("hp() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildArgs(t *testing.T) {
	cfg := &core.Config{
		Timezone: "UTC",
		Image:    "arizuko-ant:test",
	}
	mounts := []volumeMount{
		{Host: "/h/group", Container: "/home/node"},
		{Host: "/h/app", Container: "/opt/arizuko", RO: true},
	}

	args := buildArgs(cfg, mounts, "test-container", EgressConfig{}, "", "", "", 0)

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--name test-container") {
		t.Error("missing container name")
	}
	if !strings.Contains(joined, "TZ=UTC") {
		t.Error("missing timezone")
	}
	if !strings.Contains(joined, "-v /h/group:/home/node") {
		t.Error("missing rw mount")
	}
	if !strings.Contains(joined, "/h/app:/opt/arizuko:ro") {
		t.Error("missing ro mount")
	}

	last := args[len(args)-1]
	if last != "arizuko-ant:test" {
		t.Errorf("last arg = %q, want image", last)
	}

	if args[0] != "run" {
		t.Errorf("first arg = %q, want 'run'", args[0])
	}
}

func TestMigrationVersion(t *testing.T) {
	d := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		v := MigrationVersion(filepath.Join(d, "nope"))
		if v != 0 {
			t.Errorf("got %d, want 0", v)
		}
	})

	t.Run("valid version", func(t *testing.T) {
		p := filepath.Join(d, "VERSION")
		os.WriteFile(p, []byte("42\n"), 0o644)
		v := MigrationVersion(p)
		if v != 42 {
			t.Errorf("got %d, want 42", v)
		}
	})

	t.Run("whitespace", func(t *testing.T) {
		p := filepath.Join(d, "VER2")
		os.WriteFile(p, []byte("  7  \n"), 0o644)
		v := MigrationVersion(p)
		if v != 7 {
			t.Errorf("got %d, want 7", v)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		p := filepath.Join(d, "EMPTY")
		os.WriteFile(p, []byte(""), 0o644)
		v := MigrationVersion(p)
		if v != 0 {
			t.Errorf("got %d, want 0", v)
		}
	})

	t.Run("non-numeric", func(t *testing.T) {
		p := filepath.Join(d, "ALPHA")
		os.WriteFile(p, []byte("abc\n"), 0o644)
		v := MigrationVersion(p)
		if v != 0 {
			t.Errorf("got %d, want 0", v)
		}
	})
}

func TestSeedSettings(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{
		Name:    "TestBot",
		WebHost: "https://example.com",
	}
	in := Input{
		Folder:  "testgroup",
		Depth:   2,
		Channel: "telegram",
	}

	seedSettings(d, cfg, in, true)

	data, err := os.ReadFile(filepath.Join(d, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}

	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}

	env, ok := s["env"].(map[string]any)
	if !ok {
		t.Fatal("env not a map")
	}
	if env["ARIZUKO_ASSISTANT_NAME"] != "TestBot" {
		t.Errorf("name = %v", env["ARIZUKO_ASSISTANT_NAME"])
	}
	if env["ARIZUKO_IS_ROOT"] != "1" {
		t.Errorf("is_root = %v", env["ARIZUKO_IS_ROOT"])
	}
	if env["ARIZUKO_DELEGATE_DEPTH"] != "2" {
		t.Errorf("depth = %v", env["ARIZUKO_DELEGATE_DEPTH"])
	}
	if env["WEB_HOST"] != "https://example.com" {
		t.Errorf("web_host = %v", env["WEB_HOST"])
	}
	if s["outputStyle"] != "telegram" {
		t.Errorf("outputStyle = %v", s["outputStyle"])
	}
	// Root bot: world is empty, tier 0.
	if env["ARIZUKO_GROUP_FOLDER"] != "testgroup" {
		t.Errorf("group_folder = %v", env["ARIZUKO_GROUP_FOLDER"])
	}
	if env["ARIZUKO_GROUP_NAME"] != "testgroup" {
		t.Errorf("group_name (derived from folder basename) = %v", env["ARIZUKO_GROUP_NAME"])
	}
	if env["ARIZUKO_WORLD"] != "" {
		t.Errorf("world (root) = %v, want empty", env["ARIZUKO_WORLD"])
	}
	if env["ARIZUKO_TIER"] != "0" {
		t.Errorf("tier (root) = %v, want 0", env["ARIZUKO_TIER"])
	}
	// Root publishes under DATA_DIR/web/pub/ → URL /pub/<path>.
	if env["WEB_PREFIX"] != "pub" {
		t.Errorf("WEB_PREFIX (root) = %v, want pub", env["WEB_PREFIX"])
	}

	servers, ok := s["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers not a map")
	}
	nc, ok := servers["arizuko"].(map[string]any)
	if !ok {
		t.Fatal("arizuko server missing")
	}
	if nc["command"] != "socat" {
		t.Errorf("command = %v", nc["command"])
	}
}

func TestSeedSettingsNonRoot(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "atlas/support"}

	seedSettings(d, cfg, in, false)

	data, _ := os.ReadFile(filepath.Join(d, "settings.json"))
	var s map[string]any
	json.Unmarshal(data, &s)

	env := s["env"].(map[string]any)
	if env["ARIZUKO_IS_ROOT"] != "" {
		t.Errorf("non-root should have empty ARIZUKO_IS_ROOT, got %v",
			env["ARIZUKO_IS_ROOT"])
	}
	// Tier 2 building: world=atlas, parent=atlas, tier=2.
	if env["ARIZUKO_WORLD"] != "atlas" {
		t.Errorf("world = %v, want atlas", env["ARIZUKO_WORLD"])
	}
	if env["ARIZUKO_GROUP_PARENT"] != "atlas" {
		t.Errorf("parent (derived from folder path) = %v, want atlas", env["ARIZUKO_GROUP_PARENT"])
	}
	if env["ARIZUKO_GROUP_NAME"] != "support" {
		t.Errorf("name (derived from folder basename) = %v, want support", env["ARIZUKO_GROUP_NAME"])
	}
	if env["ARIZUKO_TIER"] != "2" {
		t.Errorf("tier = %v, want 2", env["ARIZUKO_TIER"])
	}
	// Tier 2 shares the parent world's web vhost.
	// WEB_PREFIX = world name so the agent knows the subdomain.
	if env["WEB_PREFIX"] != "atlas" {
		t.Errorf("WEB_PREFIX (tier 2) = %v, want atlas", env["WEB_PREFIX"])
	}
}

func TestSeedSettingsTier1World(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "atlas"}

	seedSettings(d, cfg, in, false)

	data, _ := os.ReadFile(filepath.Join(d, "settings.json"))
	var s map[string]any
	json.Unmarshal(data, &s)

	env := s["env"].(map[string]any)
	if env["ARIZUKO_WORLD"] != "atlas" {
		t.Errorf("world = %v, want atlas", env["ARIZUKO_WORLD"])
	}
	if env["ARIZUKO_TIER"] != "1" {
		t.Errorf("tier = %v, want 1", env["ARIZUKO_TIER"])
	}
	if env["ARIZUKO_GROUP_PARENT"] != "" {
		t.Errorf("parent (tier-1) = %v, want empty", env["ARIZUKO_GROUP_PARENT"])
	}
	// Tier 1 world publishes via the vhost subdomain <world>.$WEB_HOST,
	// NOT via path-prefix /pub/<world>/. WEB_PREFIX is the bare folder.
	if env["WEB_PREFIX"] != "atlas" {
		t.Errorf("WEB_PREFIX (tier 1) = %v, want atlas", env["WEB_PREFIX"])
	}
}

func TestSeedSettingsPreservesExisting(t *testing.T) {
	d := t.TempDir()
	fp := filepath.Join(d, "settings.json")
	existing := map[string]any{
		"customKey": "preserved",
		"env": map[string]any{
			"MY_VAR": "keep",
		},
	}
	data, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(fp, data, 0o644)

	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "g"}
	seedSettings(d, cfg, in, false)

	data, _ = os.ReadFile(fp)
	var s map[string]any
	json.Unmarshal(data, &s)

	if s["customKey"] != "preserved" {
		t.Error("custom key was overwritten")
	}
	env := s["env"].(map[string]any)
	if env["MY_VAR"] != "keep" {
		t.Error("existing env var was overwritten")
	}
}

// seedSettings asserts the platform's allow-all + sandbox-off posture every
// spawn, overwriting a stray agent-added permissions/sandbox block (web egress
// is crackbox, not Claude Code permissions — see ant/CLAUDE.md).
func TestSeedSettingsAssertsPosture(t *testing.T) {
	d := t.TempDir()
	fp := filepath.Join(d, "settings.json")
	stray := map[string]any{
		"permissions": map[string]any{"allow": []string{"WebFetch", "WebSearch"}},
		"sandbox":     map[string]any{"enabled": true},
	}
	data, _ := json.MarshalIndent(stray, "", "  ")
	os.WriteFile(fp, data, 0o644)

	seedSettings(d, &core.Config{Name: "Bot"}, Input{Folder: "atlas/support"}, false)

	data, _ = os.ReadFile(fp)
	var s map[string]any
	json.Unmarshal(data, &s)

	perms, ok := s["permissions"].(map[string]any)
	if !ok || perms["defaultMode"] != "bypassPermissions" {
		t.Errorf("permissions = %v, want defaultMode=bypassPermissions", s["permissions"])
	}
	if perms["allow"] != nil {
		t.Errorf("stray permissions.allow survived: %v", perms["allow"])
	}
	sb, ok := s["sandbox"].(map[string]any)
	if !ok || sb["enabled"] != false {
		t.Errorf("sandbox = %v, want enabled=false", s["sandbox"])
	}
}

func TestWriteGatewayCaps(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{
		VoiceEnabled:  true,
		WhisperModel:  "turbo",
		VideoEnabled:  false,
		MediaEnabled:  true,
		MediaMaxBytes: 20 * 1024 * 1024,
		WebHost:       "https://web.example.com",
	}

	writeGatewayCaps(d, cfg)

	data, err := os.ReadFile(filepath.Join(d, ".gateway-caps"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	checks := []string{
		"enabled = true",
		`model = "turbo"`,
		"[voice]",
		"[video]",
		"[media]",
		"max_size_mb = 20",
		"[web]",
		`host = "https://web.example.com"`,
	}
	for _, c := range checks {
		if !strings.Contains(s, c) {
			t.Errorf("missing %q in:\n%s", c, s)
		}
	}
}

func TestWriteGatewayCapsNoWeb(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{}

	writeGatewayCaps(d, cfg)

	data, _ := os.ReadFile(filepath.Join(d, ".gateway-caps"))
	s := string(data)
	if !strings.Contains(s, "[web]\nenabled = false") {
		t.Errorf("expected web disabled, got:\n%s", s)
	}
}

func TestCpDir(t *testing.T) {
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "a.txt"), []byte("hello"), 0o644)
	os.MkdirAll(filepath.Join(src, "sub"), 0o755)
	os.WriteFile(
		filepath.Join(src, "sub", "b.txt"), []byte("world"), 0o644)

	dst := filepath.Join(t.TempDir(), "out")
	cpDirOverwrite(src, dst)

	got, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("a.txt: %q, err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil || string(got) != "world" {
		t.Errorf("sub/b.txt: %q, err=%v", got, err)
	}
}

// TestCpDirOverwriteReadOnlyDst regresses the cpDirOverwrite permission bug:
// a dst file the daemon can't truncate-open (here mode 0444; in production a
// root-owned file under the uid-1000-owned dir) must still be refreshed —
// the daemon owns the directory, so it can unlink + recreate. Pre-fix the
// truncate-open WriteFile failed and the stale content survived.
func TestCpDirOverwriteReadOnlyDst(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores file permissions; can't reproduce the truncate-open failure")
	}
	src := t.TempDir()
	os.WriteFile(filepath.Join(src, "style.md"), []byte("new"), 0o644)

	dst := t.TempDir()
	os.WriteFile(filepath.Join(dst, "style.md"), []byte("stale"), 0o444)

	cpDirOverwrite(src, dst)

	got, err := os.ReadFile(filepath.Join(dst, "style.md"))
	if err != nil || string(got) != "new" {
		t.Fatalf("style.md not refreshed: %q err=%v (pre-fix: truncate-open fails on 0444)", got, err)
	}
}

func TestBuildArgsCustomUser(t *testing.T) {
	if os.Getuid() == 1000 {
		t.Skip("uid is 1000, skip custom user test")
	}
	if os.Getuid() == 0 {
		t.Skip("running as root, skip custom user test")
	}

	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	args := buildArgs(cfg, nil, "test", EgressConfig{}, "", "", "", 0)
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--user") {
		t.Error("expected --user for non-1000 uid")
	}
}

func TestInputJSON(t *testing.T) {
	in := Input{
		Prompt:    "hello",
		SessionID: "s1",
		ChatJID:   "chat@jid",
		Folder:    "mygroup",
		MsgCount:  5,
		Depth:     1,
		Channel:   "telegram",
		MessageID: "msg-42",
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	json.Unmarshal(data, &got)

	if got["prompt"] != "hello" {
		t.Errorf("prompt = %v", got["prompt"])
	}
	if got["groupFolder"] != "mygroup" {
		t.Errorf("folder = %v", got["groupFolder"])
	}
	if got["channelName"] != "telegram" {
		t.Errorf("channel = %v", got["channelName"])
	}

	if _, ok := got["groupPath"]; ok {
		t.Error("json:- field leaked into JSON")
	}
	if _, ok := got["config"]; ok {
		t.Error("json:- field leaked into JSON")
	}
}

func TestWriteLog(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "test.log")

	in := Input{
		Prompt:    "test prompt",
		Folder:    "g1",
		SessionID: "s1",
	}
	mounts := []volumeMount{
		{Host: "/h", Container: "/c"},
		{Host: "/h2", Container: "/c2", RO: true},
	}

	writeLog(p, in, "cname", 5*time.Second, 0, false, "stderr", mounts)

	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)

	if !strings.Contains(s, "Container Run Log") {
		t.Error("missing header")
	}
	if !strings.Contains(s, "Group: g1") {
		t.Error("missing group")
	}
	if !strings.Contains(s, "Container: cname") {
		t.Error("missing container name")
	}
	if !strings.Contains(s, "Exit Code: 0") {
		t.Error("missing exit code")
	}
	if strings.Contains(s, "TIMEOUT") {
		t.Error("should not show TIMEOUT")
	}
}

func TestWriteLogTimeout(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "test.log")

	writeLog(p, Input{Folder: "g"}, "c", time.Second, 1, true, "", nil)

	data, _ := os.ReadFile(p)
	s := string(data)
	if !strings.Contains(s, "TIMEOUT") {
		t.Error("missing TIMEOUT header")
	}
}

func TestSeedSkillsClaudeJSON(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	os.MkdirAll(filepath.Join(appDir, "ant", "skills"), 0o755)
	cfg := &core.Config{HostAppDir: appDir}

	seedSkills(cfg, claudeDir, "mygroup")

	p := filepath.Join(claudeDir, ".claude.json")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf(".claude.json not created: %v", err)
	}

	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	if _, ok := m["userID"]; !ok {
		t.Error("userID missing")
	}
	if _, ok := m["firstStartTime"]; !ok {
		t.Error("firstStartTime missing")
	}
}

func TestSeedSkillsClaudeJSON_Idempotent(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	os.MkdirAll(filepath.Join(appDir, "ant", "skills"), 0o755)
	cfg := &core.Config{HostAppDir: appDir}

	seedSkills(cfg, claudeDir, "mygroup")

	p := filepath.Join(claudeDir, ".claude.json")
	first, _ := os.ReadFile(p)

	seedSkills(cfg, claudeDir, "mygroup")
	second, _ := os.ReadFile(p)

	if string(first) != string(second) {
		t.Error("second call overwrote .claude.json")
	}
}

func TestSeedSkillsClaudeJSON_UserIDDerivedFromFolder(t *testing.T) {
	appDir := t.TempDir()
	os.MkdirAll(filepath.Join(appDir, "ant", "skills"), 0o755)
	cfg := &core.Config{HostAppDir: appDir}

	d1 := t.TempDir()
	d2 := t.TempDir()
	seedSkills(cfg, d1, "folderA")
	seedSkills(cfg, d2, "folderB")

	data1, _ := os.ReadFile(filepath.Join(d1, ".claude.json"))
	data2, _ := os.ReadFile(filepath.Join(d2, ".claude.json"))

	var m1, m2 map[string]any
	json.Unmarshal(data1, &m1)
	json.Unmarshal(data2, &m2)

	if m1["userID"] == m2["userID"] {
		t.Error("different folders should produce different userIDs")
	}
}

func TestSeedSkills_MergeBase(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	// Stock skill: ant/skills/foo/SKILL.md
	skillSrc := filepath.Join(appDir, "ant", "skills", "foo")
	os.MkdirAll(skillSrc, 0o755)
	os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte("stock-foo"), 0o644)
	// Stock CLAUDE.md
	os.WriteFile(filepath.Join(appDir, "ant", "CLAUDE.md"), []byte("stock-claude"), 0o644)

	cfg := &core.Config{HostAppDir: appDir}
	seedSkills(cfg, claudeDir, "g")

	// .merge-base/CLAUDE.md matches source
	bClaude, err := os.ReadFile(filepath.Join(claudeDir, ".merge-base", "CLAUDE.md"))
	if err != nil || string(bClaude) != "stock-claude" {
		t.Errorf(".merge-base/CLAUDE.md: %q err=%v", bClaude, err)
	}
	// .merge-base/skills/foo/SKILL.md matches source
	bSkill, err := os.ReadFile(filepath.Join(claudeDir, ".merge-base", "skills", "foo", "SKILL.md"))
	if err != nil || string(bSkill) != "stock-foo" {
		t.Errorf(".merge-base/skills/foo/SKILL.md: %q err=%v", bSkill, err)
	}
	// Live copy also present
	live, _ := os.ReadFile(filepath.Join(claudeDir, "skills", "foo", "SKILL.md"))
	if string(live) != "stock-foo" {
		t.Errorf("live skill: %q", live)
	}
}

func TestSeedSkills_DotDisabled(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	skillSrc := filepath.Join(appDir, "ant", "skills", "foo")
	os.MkdirAll(skillSrc, 0o755)
	os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte("v2"), 0o644)
	os.WriteFile(filepath.Join(skillSrc, "helper.md"), []byte("h2"), 0o644)

	// Pre-existing target dir with .disabled sentinel + a stale SKILL.md
	// and an operator-kept helper file.
	tgt := filepath.Join(claudeDir, "skills", "foo")
	os.MkdirAll(tgt, 0o755)
	os.WriteFile(filepath.Join(tgt, ".disabled"), []byte(""), 0o644)
	os.WriteFile(filepath.Join(tgt, "SKILL.md"), []byte("stale"), 0o644)
	os.WriteFile(filepath.Join(tgt, "operator.md"), []byte("op"), 0o644)

	cfg := &core.Config{HostAppDir: appDir}
	seedSkills(cfg, claudeDir, "g")

	// SKILL.md must be removed (so Claude Code stops indexing).
	if _, err := os.Stat(filepath.Join(tgt, "SKILL.md")); !os.IsNotExist(err) {
		t.Error("SKILL.md should be removed when .disabled present")
	}
	// Operator files preserved.
	if data, err := os.ReadFile(filepath.Join(tgt, "operator.md")); err != nil || string(data) != "op" {
		t.Errorf("operator.md should be preserved: %q err=%v", data, err)
	}
	// Stock helper must NOT have been seeded.
	if _, err := os.Stat(filepath.Join(tgt, "helper.md")); !os.IsNotExist(err) {
		t.Error("helper.md should not have been seeded for disabled skill")
	}
	// Merge-base must NOT include the disabled skill (we skip the whole dir).
	if _, err := os.Stat(filepath.Join(claudeDir, ".merge-base", "skills", "foo")); !os.IsNotExist(err) {
		t.Error(".merge-base must skip disabled skill")
	}
}

// S1: re-running seedSkills must NOT clobber operator edits in ours/.
func TestSeedSkills_PreservesOursOnRerun(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	skillSrc := filepath.Join(appDir, "ant", "skills", "foo")
	os.MkdirAll(skillSrc, 0o755)
	os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte("upstream-v1"), 0o644)
	os.WriteFile(filepath.Join(appDir, "ant", "CLAUDE.md"), []byte("upstream-claude-v1"), 0o644)

	cfg := &core.Config{HostAppDir: appDir}
	seedSkills(cfg, claudeDir, "g")

	// Operator edits ours after first seed.
	liveSkill := filepath.Join(claudeDir, "skills", "foo", "SKILL.md")
	liveClaude := filepath.Join(claudeDir, "CLAUDE.md")
	os.WriteFile(liveSkill, []byte("operator-edit"), 0o644)
	os.WriteFile(liveClaude, []byte("operator-claude"), 0o644)

	// Upstream changes; re-seed (simulating re-invite / re-spawn).
	os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte("upstream-v2"), 0o644)
	os.WriteFile(filepath.Join(appDir, "ant", "CLAUDE.md"), []byte("upstream-claude-v2"), 0o644)
	seedSkills(cfg, claudeDir, "g")

	if data, _ := os.ReadFile(liveSkill); string(data) != "operator-edit" {
		t.Errorf("ours skill clobbered: %q", data)
	}
	if data, _ := os.ReadFile(liveClaude); string(data) != "operator-claude" {
		t.Errorf("ours CLAUDE.md clobbered: %q", data)
	}
	// Merge-base must mirror upstream-v2.
	bSkill, _ := os.ReadFile(filepath.Join(claudeDir, ".merge-base", "skills", "foo", "SKILL.md"))
	if string(bSkill) != "upstream-v2" {
		t.Errorf("merge-base skill not refreshed: %q", bSkill)
	}
	bClaude, _ := os.ReadFile(filepath.Join(claudeDir, ".merge-base", "CLAUDE.md"))
	if string(bClaude) != "upstream-claude-v2" {
		t.Errorf("merge-base CLAUDE.md not refreshed: %q", bClaude)
	}
}

// S2/S3: merge-base/ must mirror upstream — stale files from prior
// versions (renames / deletes) must not accumulate forever.
func TestSeedSkills_MergeBaseMirrorsUpstream(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	skillSrc := filepath.Join(appDir, "ant", "skills", "foo")
	os.MkdirAll(skillSrc, 0o755)
	os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte("v1"), 0o644)
	os.WriteFile(filepath.Join(skillSrc, "helper.md"), []byte("h1"), 0o644)

	cfg := &core.Config{HostAppDir: appDir}
	seedSkills(cfg, claudeDir, "g")

	baseHelper := filepath.Join(claudeDir, ".merge-base", "skills", "foo", "helper.md")
	if _, err := os.Stat(baseHelper); err != nil {
		t.Fatalf("base helper.md missing: %v", err)
	}

	// Upstream renames helper.md → helper2.md.
	os.Remove(filepath.Join(skillSrc, "helper.md"))
	os.WriteFile(filepath.Join(skillSrc, "helper2.md"), []byte("h2"), 0o644)
	seedSkills(cfg, claudeDir, "g")

	// Old helper.md must be GONE from merge-base.
	if _, err := os.Stat(baseHelper); !os.IsNotExist(err) {
		t.Error("merge-base helper.md should have been removed (upstream renamed it)")
	}
	if _, err := os.Stat(filepath.Join(claudeDir, ".merge-base", "skills", "foo", "helper2.md")); err != nil {
		t.Errorf("merge-base helper2.md missing: %v", err)
	}
}

// S8: the migrate bootstrap skill is platform-owned — a stale on-disk copy
// (e.g. the pre-split version reading the removed /workspace mount) must be
// force-overwritten on spawn, or migration deadlocks. Operator edits to it
// are NOT preserved (unlike stock skills).
func TestSeedMigrateSkill_OverwritesStale(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	migSrc := filepath.Join(appDir, "ant", "skills", "migrate")
	os.MkdirAll(migSrc, 0o755)
	os.WriteFile(filepath.Join(migSrc, "SKILL.md"), []byte("theirs = /opt/arizuko/ant"), 0o644)

	// Group carries the pre-split migrate skill referencing /workspace.
	liveMig := filepath.Join(claudeDir, "skills", "migrate", "SKILL.md")
	os.MkdirAll(filepath.Dir(liveMig), 0o755)
	os.WriteFile(liveMig, []byte("theirs = /workspace/self/ant"), 0o644)

	seedMigrateSkill(&core.Config{HostAppDir: appDir}, claudeDir)

	if data, _ := os.ReadFile(liveMig); string(data) != "theirs = /opt/arizuko/ant" {
		t.Errorf("stale migrate skill not refreshed: %q", data)
	}
}

// S7: re-enabling a previously .disabled skill must start from a fresh
// merge-base (the stale base must be removed when seedSkills sees
// .disabled).
func TestSeedSkills_DisabledRemovesMergeBase(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	skillSrc := filepath.Join(appDir, "ant", "skills", "foo")
	os.MkdirAll(skillSrc, 0o755)
	os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte("v1"), 0o644)

	cfg := &core.Config{HostAppDir: appDir}
	seedSkills(cfg, claudeDir, "g")

	// Confirm merge-base exists.
	base := filepath.Join(claudeDir, ".merge-base", "skills", "foo")
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("merge-base not seeded: %v", err)
	}

	// Operator disables the skill.
	tgt := filepath.Join(claudeDir, "skills", "foo")
	os.WriteFile(filepath.Join(tgt, ".disabled"), []byte(""), 0o644)
	seedSkills(cfg, claudeDir, "g")

	// Merge-base must be wiped so re-enable starts fresh.
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Error("merge-base must be removed when .disabled sentinel present")
	}
}

func TestReadOptional(t *testing.T) {
	d := t.TempDir()

	t.Run("missing file returns empty", func(t *testing.T) {
		got := readOptional(filepath.Join(d, "nonexistent.md"))
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("existing file returns trimmed content", func(t *testing.T) {
		p := filepath.Join(d, "PERSONA.md")
		os.WriteFile(p, []byte("  be kind\n"), 0o644)
		got := readOptional(p)
		if got != "be kind" {
			t.Errorf("got %q, want %q", got, "be kind")
		}
	})
}

func TestInputJSONNewFields(t *testing.T) {
	in := Input{
		Prompt:   "hello",
		Folder:   "g",
		ChatJID:  "chat@jid",
		Sender:   "telegram:123",
		Persona:  "be kind",
		SystemMd: "you are an agent",
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}

	var got map[string]any
	json.Unmarshal(data, &got)

	if got["sender"] != "telegram:123" {
		t.Errorf("sender = %v", got["sender"])
	}
	if got["persona"] != "be kind" {
		t.Errorf("persona = %v", got["persona"])
	}
	if got["systemMd"] != "you are an agent" {
		t.Errorf("systemMd = %v", got["systemMd"])
	}
}

func TestPersonaAndSystemMdLoading(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "PERSONA.md"), []byte("warm and friendly"), 0o644)
	os.WriteFile(filepath.Join(d, "SYSTEM.md"), []byte("custom system prompt"), 0o644)

	persona := readOptional(filepath.Join(d, "PERSONA.md"))
	if persona != "warm and friendly" {
		t.Errorf("persona = %q", persona)
	}

	sys := readOptional(filepath.Join(d, "SYSTEM.md"))
	if sys != "custom system prompt" {
		t.Errorf("systemMd = %q", sys)
	}
}

func TestPrepareInputResolveNudge(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{HostAppDir: t.TempDir()}
	os.MkdirAll(filepath.Join(cfg.HostAppDir, "ant", "skills", "self"), 0o755)

	in := Input{Prompt: "hello world"}
	out := prepareInput(cfg, in, d)

	if !strings.Contains(out.Prompt, "[resolve]") {
		t.Error("resolve nudge missing from prepared prompt")
	}
	if !strings.HasSuffix(out.Prompt, "hello world") {
		t.Errorf("user prompt not at end: %q", out.Prompt[len(out.Prompt)-40:])
	}
}

func TestPrepareInputInjectsWorkMd(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{HostAppDir: t.TempDir()}
	os.MkdirAll(filepath.Join(cfg.HostAppDir, "ant", "skills", "self"), 0o755)
	os.WriteFile(filepath.Join(d, "work.md"), []byte("## Current task\nfixing bug"), 0o644)

	out := prepareInput(cfg, Input{Prompt: "hi"}, d)

	if !strings.Contains(out.Prompt, `<knowledge layer="work">`) {
		t.Errorf("work.md not injected: %q", out.Prompt)
	}
	if !strings.Contains(out.Prompt, "fixing bug") {
		t.Errorf("work.md content missing from prompt")
	}
}

func TestPrepareInputNoWorkMd(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{HostAppDir: t.TempDir()}
	os.MkdirAll(filepath.Join(cfg.HostAppDir, "ant", "skills", "self"), 0o755)

	out := prepareInput(cfg, Input{Prompt: "hi"}, d)

	if strings.Contains(out.Prompt, `layer="work"`) {
		t.Errorf("work layer should be absent when no work.md")
	}
}

func TestPersonaAndSystemMdMissing(t *testing.T) {
	d := t.TempDir()

	persona := readOptional(filepath.Join(d, "PERSONA.md"))
	if persona != "" {
		t.Errorf("expected empty persona, got %q", persona)
	}

	sys := readOptional(filepath.Join(d, "SYSTEM.md"))
	if sys != "" {
		t.Errorf("expected empty systemMd, got %q", sys)
	}
}
