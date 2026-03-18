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

func TestReadonlyMountArgs(t *testing.T) {
	got := ReadonlyMountArgs("/host/path", "/container/path")
	want := []string{"-v", "/host/path:/container/path:ro"}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestStopContainerArgs(t *testing.T) {
	got := StopContainerArgs("arizuko-test-123")
	if got[0] != "stop" || got[1] != "arizuko-test-123" {
		t.Errorf("got %v", got)
	}
}

func TestSidecarName(t *testing.T) {
	cases := []struct {
		folder, name, want string
	}{
		{"mygroup", "redis", "arizuko-sc-mygroup-redis"},
		{"my/sub", "pg", "arizuko-sc-my-sub-pg"},
		{"a@b", "x", "arizuko-sc-a-b-x"},
	}
	for _, tc := range cases {
		got := sidecarName(tc.folder, tc.name)
		if got != tc.want {
			t.Errorf("sidecarName(%q, %q) = %q, want %q",
				tc.folder, tc.name, got, tc.want)
		}
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
			core.Config{DataDir: "/srv/data/inst/data"},
			"/srv/data/inst/data/sessions/g",
			"/srv/data/inst/data/sessions/g",
		},
		{
			"with host root",
			core.Config{
				HostProjectRoot: "/host/inst",
				DataDir:         "/srv/data/inst/data",
			},
			"/srv/data/inst/data/sessions/g",
			"/host/inst/data/sessions/g",
		},
		{
			"path outside project",
			core.Config{
				HostProjectRoot: "/host/inst",
				DataDir:         "/srv/data/inst/data",
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
		Image:    "arizuko-agent:test",
	}
	mounts := []VolumeMount{
		{Host: "/h/group", Container: "/workspace/group"},
		{Host: "/h/app", Container: "/workspace/self", RO: true},
	}

	args := buildArgs(cfg, mounts, "test-container")

	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--name test-container") {
		t.Error("missing container name")
	}
	if !strings.Contains(joined, "TZ=UTC") {
		t.Error("missing timezone")
	}
	if !strings.Contains(joined, "-v /h/group:/workspace/group") {
		t.Error("missing rw mount")
	}
	if !strings.Contains(joined, "/h/app:/workspace/self:ro") {
		t.Error("missing ro mount")
	}

	last := args[len(args)-1]
	if last != "arizuko-agent:test" {
		t.Errorf("last arg = %q, want image", last)
	}

	if args[0] != "run" {
		t.Errorf("first arg = %q, want 'run'", args[0])
	}
}

func TestMigrationVersion(t *testing.T) {
	d := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		v := migrationVersion(filepath.Join(d, "nope"))
		if v != 0 {
			t.Errorf("got %d, want 0", v)
		}
	})

	t.Run("valid version", func(t *testing.T) {
		p := filepath.Join(d, "VERSION")
		os.WriteFile(p, []byte("42\n"), 0o644)
		v := migrationVersion(p)
		if v != 42 {
			t.Errorf("got %d, want 42", v)
		}
	})

	t.Run("whitespace", func(t *testing.T) {
		p := filepath.Join(d, "VER2")
		os.WriteFile(p, []byte("  7  \n"), 0o644)
		v := migrationVersion(p)
		if v != 7 {
			t.Errorf("got %d, want 7", v)
		}
	})

	t.Run("empty file", func(t *testing.T) {
		p := filepath.Join(d, "EMPTY")
		os.WriteFile(p, []byte(""), 0o644)
		v := migrationVersion(p)
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
	if env["NANOCLAW_ASSISTANT_NAME"] != "TestBot" {
		t.Errorf("name = %v", env["NANOCLAW_ASSISTANT_NAME"])
	}
	if env["NANOCLAW_IS_ROOT"] != "1" {
		t.Errorf("is_root = %v", env["NANOCLAW_IS_ROOT"])
	}
	if env["NANOCLAW_DELEGATE_DEPTH"] != "2" {
		t.Errorf("depth = %v", env["NANOCLAW_DELEGATE_DEPTH"])
	}
	if env["WEB_HOST"] != "https://example.com" {
		t.Errorf("web_host = %v", env["WEB_HOST"])
	}
	if s["outputStyle"] != "telegram" {
		t.Errorf("outputStyle = %v", s["outputStyle"])
	}

	servers, ok := s["mcpServers"].(map[string]any)
	if !ok {
		t.Fatal("mcpServers not a map")
	}
	nc, ok := servers["nanoclaw"].(map[string]any)
	if !ok {
		t.Fatal("nanoclaw server missing")
	}
	if nc["command"] != "socat" {
		t.Errorf("command = %v", nc["command"])
	}
}

func TestSeedSettingsNonRoot(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "parent/child"}

	seedSettings(d, cfg, in, false)

	data, _ := os.ReadFile(filepath.Join(d, "settings.json"))
	var s map[string]any
	json.Unmarshal(data, &s)

	env := s["env"].(map[string]any)
	if env["NANOCLAW_IS_ROOT"] != "" {
		t.Errorf("non-root should have empty NANOCLAW_IS_ROOT, got %v",
			env["NANOCLAW_IS_ROOT"])
	}
}

func TestSeedSettingsWithSidecars(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{
		Folder: "g",
		Config: core.GroupConfig{
			Sidecars: map[string]core.Sidecar{
				"redis": {
					Image: "redis:7",
					Tools: []string{"get", "set"},
				},
			},
		},
	}

	seedSettings(d, cfg, in, false)

	data, _ := os.ReadFile(filepath.Join(d, "settings.json"))
	var s map[string]any
	json.Unmarshal(data, &s)

	servers := s["mcpServers"].(map[string]any)
	if _, ok := servers["redis"]; !ok {
		t.Error("redis sidecar server missing")
	}

	allowed, ok := s["allowedTools"].([]any)
	if !ok {
		t.Fatal("allowedTools missing")
	}
	found := map[string]bool{}
	for _, a := range allowed {
		found[a.(string)] = true
	}
	if !found["mcp__redis__get"] || !found["mcp__redis__set"] {
		t.Errorf("allowedTools = %v", allowed)
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
	args := buildArgs(cfg, nil, "test")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--user") {
		t.Error("expected --user for non-1000 uid")
	}
}

func TestOutputMarkerParsing(t *testing.T) {
	payload := Output{
		Status:       "success",
		Result:       "done",
		NewSessionID: "sess-123",
	}
	js, _ := json.Marshal(payload)
	raw := "some noise\n" +
		outputStartMarker + "\n" + string(js) + "\n" +
		outputEndMarker + "\nmore noise"

	si := strings.LastIndex(raw, outputStartMarker)
	ei := strings.LastIndex(raw, outputEndMarker)
	if si == -1 || ei <= si {
		t.Fatal("markers not found")
	}

	extracted := strings.TrimSpace(
		raw[si+len(outputStartMarker) : ei])
	var got Output
	if err := json.Unmarshal([]byte(extracted), &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != "success" {
		t.Errorf("status = %q", got.Status)
	}
	if got.Result != "done" {
		t.Errorf("result = %q", got.Result)
	}
	if got.NewSessionID != "sess-123" {
		t.Errorf("session = %q", got.NewSessionID)
	}
}

func TestOutputMarkerStreaming(t *testing.T) {
	p1 := Output{Result: "first", Status: "streaming"}
	p2 := Output{Result: "second", Status: "success", NewSessionID: "s2"}
	js1, _ := json.Marshal(p1)
	js2, _ := json.Marshal(p2)

	raw := outputStartMarker + string(js1) + outputEndMarker +
		"noise" +
		outputStartMarker + string(js2) + outputEndMarker

	var results []Output
	var buf strings.Builder
	buf.WriteString(raw)

	for {
		s := buf.String()
		si := strings.Index(s, outputStartMarker)
		if si == -1 {
			break
		}
		ei := strings.Index(s[si:], outputEndMarker)
		if ei == -1 {
			break
		}
		ei += si

		js := strings.TrimSpace(s[si+len(outputStartMarker) : ei])
		rest := s[ei+len(outputEndMarker):]
		buf.Reset()
		buf.WriteString(rest)

		var o Output
		if err := json.Unmarshal([]byte(js), &o); err != nil {
			t.Fatal(err)
		}
		results = append(results, o)
	}

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].Result != "first" {
		t.Errorf("[0].Result = %q", results[0].Result)
	}
	if results[1].NewSessionID != "s2" {
		t.Errorf("[1].SessionID = %q", results[1].NewSessionID)
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
	mounts := []VolumeMount{
		{Host: "/h", Container: "/c"},
		{Host: "/h2", Container: "/c2", RO: true},
	}

	writeLog(p, in, "cname", 5*time.Second, 0,
		false, true, "stdout", "stderr", mounts)

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

	writeLog(p, Input{Folder: "g"}, "c", time.Second, 1,
		true, false, "", "", nil)

	data, _ := os.ReadFile(p)
	s := string(data)
	if !strings.Contains(s, "TIMEOUT") {
		t.Error("missing TIMEOUT header")
	}
	if !strings.Contains(s, "Had Streaming Output: false") {
		t.Error("missing streaming output flag")
	}
}

func TestSeedSkillsClaudeJSON(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	os.MkdirAll(filepath.Join(appDir, "container", "skills"), 0o755)
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
	if m["thinkingMigrationComplete"] != true {
		t.Errorf("thinkingMigrationComplete = %v", m["thinkingMigrationComplete"])
	}
	if m["sonnet45MigrationComplete"] != true {
		t.Errorf("sonnet45MigrationComplete = %v", m["sonnet45MigrationComplete"])
	}
}

func TestSeedSkillsClaudeJSON_Idempotent(t *testing.T) {
	claudeDir := t.TempDir()
	appDir := t.TempDir()
	os.MkdirAll(filepath.Join(appDir, "container", "skills"), 0o755)
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
	os.MkdirAll(filepath.Join(appDir, "container", "skills"), 0o755)
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
