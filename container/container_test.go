package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
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
		{Host: "/h/app", Container: "/workspace/self", RO: true},
	}

	args := buildArgs(cfg, mounts, "test-container", "")

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
	if !strings.Contains(joined, "/h/app:/workspace/self:ro") {
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
		Folder:    "testgroup",
		GroupName: "Test World",
		Depth:     2,
		Channel:   "telegram",
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
	if env["ARIZUKO_GROUP_NAME"] != "Test World" {
		t.Errorf("group_name = %v", env["ARIZUKO_GROUP_NAME"])
	}
	if env["ARIZUKO_WORLD"] != "" {
		t.Errorf("world (root) = %v, want empty", env["ARIZUKO_WORLD"])
	}
	if env["ARIZUKO_TIER"] != "0" {
		t.Errorf("tier (root) = %v, want 0", env["ARIZUKO_TIER"])
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
	in := Input{
		Folder:    "atlas/support",
		GroupName: "Support",
		Parent:    "atlas",
	}

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
		t.Errorf("parent = %v, want atlas", env["ARIZUKO_GROUP_PARENT"])
	}
	if env["ARIZUKO_TIER"] != "2" {
		t.Errorf("tier = %v, want 2", env["ARIZUKO_TIER"])
	}
}

func TestSeedSettingsTier1World(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "atlas", GroupName: "Atlas"}

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
	cpDir(src, dst)

	got, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil || string(got) != "hello" {
		t.Errorf("a.txt: %q, err=%v", got, err)
	}
	got, err = os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil || string(got) != "world" {
		t.Errorf("sub/b.txt: %q, err=%v", got, err)
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
	args := buildArgs(cfg, nil, "test", "")
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

func TestReadOptional(t *testing.T) {
	d := t.TempDir()

	t.Run("missing file returns empty", func(t *testing.T) {
		got := readOptional(filepath.Join(d, "nonexistent.md"))
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("existing file returns trimmed content", func(t *testing.T) {
		p := filepath.Join(d, "SOUL.md")
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
		Soul:     "be kind",
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
	if got["soul"] != "be kind" {
		t.Errorf("soul = %v", got["soul"])
	}
	if got["systemMd"] != "you are an agent" {
		t.Errorf("systemMd = %v", got["systemMd"])
	}
}

func TestSoulAndSystemMdLoading(t *testing.T) {
	d := t.TempDir()
	os.WriteFile(filepath.Join(d, "SOUL.md"), []byte("warm and friendly"), 0o644)
	os.WriteFile(filepath.Join(d, "SYSTEM.md"), []byte("custom system prompt"), 0o644)

	soul := readOptional(filepath.Join(d, "SOUL.md"))
	if soul != "warm and friendly" {
		t.Errorf("soul = %q", soul)
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

func TestSoulAndSystemMdMissing(t *testing.T) {
	d := t.TempDir()

	soul := readOptional(filepath.Join(d, "SOUL.md"))
	if soul != "" {
		t.Errorf("expected empty soul, got %q", soul)
	}

	sys := readOptional(filepath.Join(d, "SYSTEM.md"))
	if sys != "" {
		t.Errorf("expected empty systemMd, got %q", sys)
	}
}
