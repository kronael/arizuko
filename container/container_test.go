package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	WriteGatewayCaps(d, cfg)

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

	WriteGatewayCaps(d, cfg)

	data, _ := os.ReadFile(filepath.Join(d, ".gateway-caps"))
	s := string(data)
	if !strings.Contains(s, "[web]\nenabled = false") {
		t.Errorf("expected web disabled, got:\n%s", s)
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
