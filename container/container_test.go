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

func TestStopContainerArgs(t *testing.T) {
	got := StopContainerArgs("arizuko-test-123")
	if got[0] != "stop" || got[1] != "arizuko-test-123" {
		t.Errorf("got %v", got)
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
	skillSrc := filepath.Join(appDir, "ant", "skills", "foo")
	os.MkdirAll(skillSrc, 0o755)
	os.WriteFile(filepath.Join(skillSrc, "SKILL.md"), []byte("stock-foo"), 0o644)
	os.WriteFile(filepath.Join(appDir, "ant", "CLAUDE.md"), []byte("stock-claude"), 0o644)

	cfg := &core.Config{HostAppDir: appDir}
	seedSkills(cfg, claudeDir, "g")

	bClaude, err := os.ReadFile(filepath.Join(claudeDir, ".merge-base", "CLAUDE.md"))
	if err != nil || string(bClaude) != "stock-claude" {
		t.Errorf(".merge-base/CLAUDE.md: %q err=%v", bClaude, err)
	}
	bSkill, err := os.ReadFile(filepath.Join(claudeDir, ".merge-base", "skills", "foo", "SKILL.md"))
	if err != nil || string(bSkill) != "stock-foo" {
		t.Errorf(".merge-base/skills/foo/SKILL.md: %q err=%v", bSkill, err)
	}
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
	if data, err := os.ReadFile(filepath.Join(tgt, "operator.md")); err != nil || string(data) != "op" {
		t.Errorf("operator.md should be preserved: %q err=%v", data, err)
	}
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

	SeedMigrateSkill(&core.Config{HostAppDir: appDir}, claudeDir)

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

	base := filepath.Join(claudeDir, ".merge-base", "skills", "foo")
	if _, err := os.Stat(base); err != nil {
		t.Fatalf("merge-base not seeded: %v", err)
	}

	tgt := filepath.Join(claudeDir, "skills", "foo")
	os.WriteFile(filepath.Join(tgt, ".disabled"), []byte(""), 0o644)
	seedSkills(cfg, claudeDir, "g")

	// Merge-base must be wiped so re-enable starts fresh.
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Error("merge-base must be removed when .disabled sentinel present")
	}
}

// TestSeedSkillsPackageLayer: a package skill staged at <datadir>/skills/<name>/
// is layered into every group's .claude/skills (spec 5/28-packages skills asset kind).
func TestSeedSkillsPackageLayer(t *testing.T) {
	dataDir := t.TempDir()
	appDir := t.TempDir()
	// The group's own .claude, not a free-standing dir: the package layer
	// resolves it from the folder, so a test that passed an unrelated path was
	// asserting on a placement the real caller never produces.
	claudeDir := filepath.Join(dataDir, "groups", "g", ".claude")
	if err := os.MkdirAll(filepath.Join(appDir, "ant", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "skills", "pkgskill"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "skills", "pkgskill", "SKILL.md"), []byte("# pkgskill"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{HostAppDir: appDir, GroupsDir: filepath.Join(dataDir, "groups")}

	seedSkills(cfg, claudeDir, "g")

	if _, err := os.Stat(filepath.Join(claudeDir, "skills", "pkgskill", "SKILL.md")); err != nil {
		t.Fatalf("package skill not layered into group: %v", err)
	}
}
