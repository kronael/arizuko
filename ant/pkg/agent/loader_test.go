package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFolder_OK(t *testing.T) {
	dir := t.TempDir()
	f, err := LoadFolder(dir)
	if err != nil {
		t.Fatalf("LoadFolder: %v", err)
	}
	if f.Root != dir {
		// On macOS TempDir may include /private prefix after Abs; allow either.
		abs, _ := filepath.Abs(dir)
		if f.Root != abs {
			t.Fatalf("Root = %q, want %q (or %q)", f.Root, dir, abs)
		}
	}
	checks := map[string]string{
		"Soul":      filepath.Join(f.Root, "SOUL.md"),
		"ClaudeMD":  filepath.Join(f.Root, "CLAUDE.md"),
		"Skills":    filepath.Join(f.Root, "skills"),
		"Diary":     filepath.Join(f.Root, "diary"),
		"Secrets":   filepath.Join(f.Root, "secrets"),
		"MCP":       filepath.Join(f.Root, "MCP.json"),
		"Workspace": filepath.Join(f.Root, "workspace"),
	}
	got := map[string]string{
		"Soul":      f.Soul,
		"ClaudeMD":  f.ClaudeMD,
		"Skills":    f.Skills,
		"Diary":     f.Diary,
		"Secrets":   f.Secrets,
		"MCP":       f.MCP,
		"Workspace": f.Workspace,
	}
	for k, want := range checks {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
}

func TestLoadFolder_NotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "no-such-folder")
	_, err := LoadFolder(missing)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestLoadFolder_NotDir(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFolder(file)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
