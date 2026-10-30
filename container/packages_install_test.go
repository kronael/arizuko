package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

func TestInstallPackageBuildsOnceAndReturnsSkills(t *testing.T) {
	source := t.TempDir()
	dataDir := t.TempDir()
	counter := filepath.Join(t.TempDir(), "counter")
	t.Setenv("INSTALL_COUNTER", counter)
	manifest := "name: example\ninstall: |\n  echo run >> \"$INSTALL_COUNTER\"\n  mkdir -p \"$PREFIX/bin\"\n  printf '#!/bin/sh\\necho built\\n' > \"$PREFIX/bin/tool\"\n  chmod +x \"$PREFIX/bin/tool\"\n"
	if err := os.WriteFile(filepath.Join(source, ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(source, "skills", "sample"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "skills", "sample", "SKILL.md"), []byte("skill"), 0o644); err != nil {
		t.Fatal(err)
	}
	installed, skills, err := InstallPackage(dataDir, source, true)
	if err != nil {
		t.Fatal(err)
	}
	if installed.Name != "example" || !reflect.DeepEqual(skills, []string{"sample"}) {
		t.Fatalf("installed %q skills %v", installed.Name, skills)
	}
	count, err := os.ReadFile(counter)
	if err != nil || string(count) != "run\n" {
		t.Fatalf("counter %q: %v", count, err)
	}
	output, err := exec.Command(filepath.Join(dataDir, "packages", "example", "bin", "tool")).Output()
	if err != nil || string(output) != "built\n" {
		t.Fatalf("tool output %q: %v", output, err)
	}
}

// A root run (sudo arizuko package) must leave the tree to the uid routd runs
// as, or routd cannot write packages/core at its next start. Runs only as root.
func TestInstallPackageHandsTreeToDaemonUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("needs root to create files owned by another uid")
	}
	source := t.TempDir()
	dataDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, ManifestFile), []byte("name: example\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := InstallPackage(dataDir, source, false); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(dataDir, "packages"),
		filepath.Join(dataDir, "packages", "example"),
		filepath.Join(dataDir, "packages", "example", ManifestFile),
	} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		if uid := int(info.Sys().(*syscall.Stat_t).Uid); uid != containerUID {
			t.Errorf("%s owned by uid %d, want %d", path, uid, containerUID)
		}
	}
}

func gitInit(t *testing.T, dir string, tracked ...string) {
	t.Helper()
	for _, args := range [][]string{{"init", "-q"}, append([]string{"add"}, tracked...)} {
		if output, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s (%v)", args, output, err)
		}
	}
}

func writeSkillTree(t *testing.T, source string, files map[string]string) {
	t.Helper()
	for path, body := range files {
		full := filepath.Join(source, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestInstallPackageIgnoresUntrackedSkillDirs(t *testing.T) {
	source, dataDir := t.TempDir(), t.TempDir()
	writeSkillTree(t, source, map[string]string{
		ManifestFile:                    "name: example\n",
		".gitignore":                    "skills/ignored/\n",
		"skills/sample/SKILL.md":        "skill",
		"skills/ignored/notes.txt":      "leftover",
		"skills/untracked/leftover.txt": "leftover",
		"skills/untracked-empty/.keep":  "",
	})
	if err := os.MkdirAll(filepath.Join(source, "skills", "empty"), 0o755); err != nil {
		t.Fatal(err)
	}
	gitInit(t, source, ManifestFile, ".gitignore", "skills/sample")
	_, skills, err := InstallPackage(dataDir, source, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(skills, []string{"sample"}) {
		t.Fatalf("installed skills %v, want [sample]", skills)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "packages", "example", "skills"))
	if err != nil || len(entries) != 1 || entries[0].Name() != "sample" {
		t.Fatalf("installed skills dir holds %v: %v", entries, err)
	}
}

func TestInstallPackageRefusesTrackedSkillWithoutSkillFile(t *testing.T) {
	source, dataDir := t.TempDir(), t.TempDir()
	writeSkillTree(t, source, map[string]string{
		ManifestFile:              "name: example\n",
		"skills/sample/SKILL.md":  "skill",
		"skills/broken/notes.txt": "tracked, no SKILL.md",
	})
	gitInit(t, source, ManifestFile, "skills")
	if _, _, err := InstallPackage(dataDir, source, false); err == nil {
		t.Fatal("a tracked skill directory without SKILL.md was installed")
	}
}

func TestInstallPackageRefusesSkillWithoutSkillFileOutsideGit(t *testing.T) {
	source, dataDir := t.TempDir(), t.TempDir()
	writeSkillTree(t, source, map[string]string{
		ManifestFile:              "name: example\n",
		"skills/sample/SKILL.md":  "skill",
		"skills/broken/notes.txt": "no SKILL.md",
	})
	if _, _, err := InstallPackage(dataDir, source, false); err == nil {
		t.Fatal("a skill directory without SKILL.md was installed from a tree git does not track")
	}
}

func TestInstallPackageFailsWhenGitIsMissingInACheckout(t *testing.T) {
	source, dataDir := t.TempDir(), t.TempDir()
	writeSkillTree(t, source, map[string]string{
		ManifestFile:             "name: example\n",
		".git/HEAD":              "ref: refs/heads/main\n",
		"skills/sample/SKILL.md": "skill",
	})
	t.Setenv("PATH", t.TempDir())
	if _, _, err := InstallPackage(dataDir, source, false); err == nil {
		t.Fatal("a checkout was read with git missing and every directory counted")
	}
}

func TestInstallPackageWithoutGitReadsEveryDirectoryOutsideACheckout(t *testing.T) {
	source, dataDir := t.TempDir(), t.TempDir()
	writeSkillTree(t, source, map[string]string{
		ManifestFile:             "name: example\n",
		"skills/sample/SKILL.md": "skill",
	})
	t.Setenv("PATH", t.TempDir())
	_, skills, err := InstallPackage(dataDir, source, false)
	if err != nil || !reflect.DeepEqual(skills, []string{"sample"}) {
		t.Fatalf("skills %v: %v", skills, err)
	}
}

func TestInstallPackageFailsOnAGitError(t *testing.T) {
	source, dataDir := t.TempDir(), t.TempDir()
	writeSkillTree(t, source, map[string]string{
		ManifestFile:             "name: example\n",
		".git":                   "not a gitfile\n",
		"skills/sample/SKILL.md": "skill",
	})
	if _, _, err := InstallPackage(dataDir, source, false); err == nil {
		t.Fatal("a git failure was read as no tracked skills")
	}
}
