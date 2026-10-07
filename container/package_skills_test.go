package container

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
)

// BUGS L47: a package's skill reached a group only if the package was already
// installed when that group was CREATED — seedSkills has one non-test caller,
// SetupGroup, and generate was not among them — and cpDirFresh skipped existing
// files, so even a re-run could not update one. These tests are the two halves:
// it must arrive late, and it must arrive again when it changes.

// pkgSkillFixture stages an instance with one group dir and one package skill,
// and returns (cfg, the group's skill dir, the package's staging dir).
func pkgSkillFixture(t *testing.T, name, body string) (*core.Config, string, string) {
	t.Helper()
	dataDir := t.TempDir()
	appDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(appDir, "ant", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(dataDir, "skills", name)
	if err := os.MkdirAll(staged, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := &core.Config{HostAppDir: appDir, GroupsDir: filepath.Join(dataDir, "groups")}
	// The group already exists — the whole point of L47.
	groupSkill := filepath.Join(cfg.GroupsDir, "eng", ".claude", "skills", name)
	if err := os.MkdirAll(filepath.Join(cfg.GroupsDir, "eng", ".claude", "skills"), 0o755); err != nil {
		t.Fatal(err)
	}
	return cfg, groupSkill, staged
}

func readSkill(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("skill not seeded: %v", err)
	}
	return string(b)
}

// The headline: install a package skill into an instance whose group already
// exists, and the group gets it. Before this the group was unreachable forever.
func TestSeedPackageSkillsReachesAnExistingGroup(t *testing.T) {
	cfg, groupSkill, _ := pkgSkillFixture(t, "triage", "# triage v1")

	n, err := SeedPackageSkills(cfg, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("seeded %d skills, want 1", n)
	}
	if got := readSkill(t, groupSkill); got != "# triage v1" {
		t.Fatalf("skill body = %q", got)
	}
}

// The other half: the package ships a fix and it has to land. cpDirFresh skips
// files that already exist, so the package layer overwrites instead — the tree
// is upstream-managed, unlike the `ours` stock skills beside it.
func TestSeedPackageSkillsUpdatesAnAlreadySeededSkill(t *testing.T) {
	cfg, groupSkill, staged := pkgSkillFixture(t, "triage", "# triage v1")
	if _, err := SeedPackageSkills(cfg, "eng"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(staged, "SKILL.md"), []byte("# triage v2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "helper.md"), []byte("added later"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SeedPackageSkills(cfg, "eng"); err != nil {
		t.Fatal(err)
	}

	if got := readSkill(t, groupSkill); got != "# triage v2" {
		t.Fatalf("skill body = %q, want the upgraded content", got)
	}
	if _, err := os.Stat(filepath.Join(groupSkill, "helper.md")); err != nil {
		t.Errorf("a file the new revision added did not arrive: %v", err)
	}
}

// The operator's escape hatch survives the overwrite: dashd's per-group skill
// toggle writes `.disabled`, the same marker stock skills honour, and a seed
// that ignored it would make disabling a package skill impossible.
func TestSeedPackageSkillsHonoursDisabledMarker(t *testing.T) {
	cfg, groupSkill, staged := pkgSkillFixture(t, "triage", "# triage v1")
	if err := os.MkdirAll(groupSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groupSkill, ".disabled"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staged, "SKILL.md"), []byte("# triage v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := SeedPackageSkills(cfg, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("seeded %d skills into a group that disabled it, want 0", n)
	}
	if _, err := os.Stat(filepath.Join(groupSkill, "SKILL.md")); !os.IsNotExist(err) {
		t.Error("a disabled package skill was re-seeded")
	}
}

// The package layer sits ON TOP of the operator-owned `ours` tree, so a package
// shipping a skill under a shipped name must not overwrite it. Before the
// overwrite existed, skip-existing hid this; it must stay closed now.
func TestSeedPackageSkillsSkipsAShippedSkillName(t *testing.T) {
	cfg, groupSkill, _ := pkgSkillFixture(t, "migrate", "# hijacked")
	if err := os.MkdirAll(filepath.Join(cfg.EffectiveAppSrcDir(), "ant", "skills", "migrate"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(groupSkill, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(groupSkill, "SKILL.md"), []byte("# the real migrate"), 0o644); err != nil {
		t.Fatal(err)
	}

	n, err := SeedPackageSkills(cfg, "eng")
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("seeded %d skills, want 0 — the name is a shipped skill's", n)
	}
	if got := readSkill(t, groupSkill); got != "# the real migrate" {
		t.Fatalf("a package overwrote a shipped skill: %q", got)
	}
}

// An instance with no skills-package installed seeds nothing and errors on
// nothing — the layer is additive and inert.
func TestSeedPackageSkillsInertWithoutPackages(t *testing.T) {
	cfg := &core.Config{HostAppDir: t.TempDir(), GroupsDir: filepath.Join(t.TempDir(), "groups")}
	n, err := SeedPackageSkills(cfg, "eng")
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v, want 0/nil", n, err)
	}
}

// A package skills dir that cannot be READ is not an instance with no
// packages: answering (0, nil) for both makes a broken mount look like a bare
// instance, and BUGS L47 is exactly the bug where a package's skill never
// arrives and nothing says why.
func TestSeedPackageSkillsRefusesAnUnreadableSkillsDir(t *testing.T) {
	cfg := &core.Config{HostAppDir: t.TempDir(), GroupsDir: filepath.Join(t.TempDir(), "groups")}
	// A regular file where the dir belongs: ReadDir fails with something other
	// than "does not exist", on any uid, root included.
	if err := os.MkdirAll(filepath.Dir(PackageSkillsDir(cfg)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(PackageSkillsDir(cfg), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}
	n, err := SeedPackageSkills(cfg, "eng")
	if err == nil {
		t.Fatal("an unreadable skills dir was reported as an instance with no packages")
	}
	if n != 0 {
		t.Errorf("n=%d, want 0", n)
	}
}
