package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

// BUGS L47 end to end, as the operator meets it: install a package that ships a
// skill on an instance whose group ALREADY exists, run the `arizuko generate`
// the install told you to run, and the group has the skill. Before this, no
// group that predated the install ever received one — install/upgrade/sync all
// printed "run generate to apply", and generate did nothing for skills.
func TestGenerateSeedsPackageSkillsIntoExistingGroups(t *testing.T) {
	t.Cleanup(func() { dropInstance(t) })
	dataDir := generateInstance(t, "WEB_PORT=8095\n")
	eng := addGroupWithClaudeDir(t, dataDir, "eng")

	src := writePkg(t, filepath.Dir(dataDir), "skpkg", "")
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
	b, err := os.ReadFile(got)
	if err != nil {
		t.Fatalf("package skill never reached the pre-existing group: %v", err)
	}
	if string(b) != "# triage v1" {
		t.Fatalf("skill body = %q", b)
	}

	// The revision half: upgrade the staged skill and generate again. cpDirFresh
	// skipped existing files, so this is the case that could not happen at all.
	if err := os.WriteFile(filepath.Join(dataDir, "skills", "triage", "SKILL.md"),
		[]byte("# triage v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	generateCompose(dataDir)
	b, err = os.ReadFile(got)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "# triage v2" {
		t.Fatalf("skill body after re-generate = %q, want the new revision", b)
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
