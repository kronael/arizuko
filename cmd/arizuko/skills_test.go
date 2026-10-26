package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// Generating an instance preserves immutable package skills without creating writable group copies.
func TestGenerateKeepsPackagedSkillsInArtifacts(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	dataDir := generateInstance(t, "WEB_PORT=8095\n")
	unsealPackageFixtures(t, dataDir)
	eng := addGroupWithClaudeDir(t, dataDir, "eng")

	src := writePkg(t, filepath.Dir(dataDir), "skpkg", "")
	mustWrite(t, filepath.Join(src, "arizuko.toml"), "name = \"skpkg\"\n")
	if err := os.MkdirAll(filepath.Join(src, "skills", "triage"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "skills", "triage", "SKILL.md"),
		[]byte("# triage v1"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"g", "install", src})

	generateCompose(dataDir)

	got := filepath.Join(eng, ".claude", "skills", "triage", "SKILL.md")
	if _, err := os.Lstat(got); !os.IsNotExist(err) {
		t.Fatalf("generate copied a managed skill: %v", err)
	}
	if got := selectedSkill(t, dataDir, "", "skpkg", "triage"); got != "# triage v1" {
		t.Fatalf("skill body = %q", got)
	}

	if err := os.WriteFile(filepath.Join(src, "skills", "triage", "SKILL.md"),
		[]byte("# triage v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmdPackages([]string{"g", "upgrade", "skpkg"})
	generateCompose(dataDir)
	if _, err := os.Lstat(got); !os.IsNotExist(err) {
		t.Fatalf("generate copied an upgraded managed skill: %v", err)
	}
	if got := selectedSkill(t, dataDir, "", "skpkg", "triage"); got != "# triage v2" {
		t.Fatalf("skill body after upgrade = %q", got)
	}
}

// addGroupWithClaudeDir registers a group in routd.db and materializes its
// group dir, which is what `generate` walks. Returns the group dir.
func addGroupWithClaudeDir(t *testing.T, dataDir, folder string) string {
	t.Helper()
	t.Cleanup(func() { dropInstance(t) })
	rdb, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rdb.Close()
	if err := store.NewOver(rdb.Obj()).PutGroup(core.Group{Folder: folder}); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(dataDir, "groups", folder)
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}
