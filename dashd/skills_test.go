package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStockSkills(t *testing.T) {
	appDir := t.TempDir()
	skillsDir := filepath.Join(appDir, "ant", "skills")
	if err := os.MkdirAll(skillsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"diary", "resolve", "voice", "bad name!"} {
		if err := os.MkdirAll(filepath.Join(skillsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// place a file (not a dir) — must be ignored
	if err := os.WriteFile(filepath.Join(skillsDir, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := &dash{appDir: appDir}
	got := d.stockSkills()

	wantSet := map[string]bool{"diary": true, "resolve": true, "voice": true}
	if len(got) != len(wantSet) {
		t.Fatalf("stockSkills = %v, want 3 valid names", got)
	}
	for _, name := range got {
		if !wantSet[name] {
			t.Errorf("unexpected skill %q in stock list", name)
		}
	}
}

func TestStockSkillsNoAppDir(t *testing.T) {
	d := &dash{}
	if got := d.stockSkills(); got != nil {
		t.Errorf("expected nil with empty appDir, got %v", got)
	}
}

func TestSkillsDisabled(t *testing.T) {
	groups := t.TempDir()
	folder := "mygroup"
	skillsBase := filepath.Join(groups, folder, ".claude", "skills")

	// diary: enabled (no marker)
	if err := os.MkdirAll(filepath.Join(skillsBase, "diary"), 0o755); err != nil {
		t.Fatal(err)
	}
	// voice: disabled (marker present)
	if err := os.MkdirAll(filepath.Join(skillsBase, "voice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillsBase, "voice", ".disabled"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	d := &dash{groupsDir: groups}
	got := d.skillsDisabled(folder)
	if got["diary"] {
		t.Error("diary should not be disabled")
	}
	if !got["voice"] {
		t.Error("voice should be disabled")
	}
}

func TestSetSkillDisabled(t *testing.T) {
	groups := t.TempDir()
	folder := "mygroup"
	d := &dash{groupsDir: groups}

	// disable a skill that doesn't exist yet (dir created automatically)
	if err := d.setSkillDisabled(folder, "diary", true); err != nil {
		t.Fatalf("setSkillDisabled(true): %v", err)
	}
	marker := filepath.Join(groups, folder, ".claude", "skills", "diary", ".disabled")
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("marker not created: %v", err)
	}

	// enable it (remove marker)
	if err := d.setSkillDisabled(folder, "diary", false); err != nil {
		t.Fatalf("setSkillDisabled(false): %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Error("marker should be removed after enable")
	}

	// enable again when already enabled is a no-op
	if err := d.setSkillDisabled(folder, "diary", false); err != nil {
		t.Errorf("double enable should not error: %v", err)
	}
}

func TestSetSkillDisabledRejectsInvalidName(t *testing.T) {
	groups := t.TempDir()
	d := &dash{groupsDir: groups}
	if err := d.setSkillDisabled("g1", "bad name!", true); err == nil {
		t.Error("expected error for invalid skill name")
	}
}
