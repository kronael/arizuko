package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
