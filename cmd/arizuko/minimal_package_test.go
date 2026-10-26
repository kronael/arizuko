package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

func seedCoreSource(t *testing.T, app string) {
	t.Helper()
	for rel, data := range map[string]string{
		"arizuko.yml":          "name: core\n",
		"CLAUDE.md":            "core manual\n",
		"skills/base/SKILL.md": "base skill\n",
	} {
		path := filepath.Join(app, "ant", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("APP_SRC_DIR", app)
}

func selectedSkill(t *testing.T, dataDir, folder, name, skill string) string {
	t.Helper()
	db, err := routd.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, found, err := db.InstalledPackage(folder, name); err != nil || !found {
		t.Fatalf("package selection %s/%s: found=%v err=%v", folder, name, found, err)
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "packages", name, "skills", skill, "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestMinimalPackageInstallUpdateRemove(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := seedPkgInstance(t, base, "minimal")
	mustWrite(t, filepath.Join(dataDir, "groups", "main", "CLAUDE.md"), "custom group runbook")
	db := mustOpenRoutd()
	defer db.Close()
	database := store.NewOver(db.Obj())
	for _, folder := range []string{"main", "other"} {
		if err := database.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := container.InstallPackage(dataDir, filepath.Join(os.Getenv("APP_SRC_DIR"), "ant"), false,
		database, store.InstalledPackage{Source: "ant"}); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(base, "source-with-another-name")
	mustWrite(t, filepath.Join(source, "arizuko.yml"), `install: |-
    mkdir -p "$PREFIX/bin"
    printf '#!/bin/sh\nprintf tool-ok\n' > "$PREFIX/bin/demo"
    chmod +x "$PREFIX/bin/demo"
name: addon
env:
  NEWS_FEED_URL: {required: true, hint: Encrypted group secret}
  NEWS_FEED_TOKEN: {required: true, hint: Encrypted group secret}
`)
	mustWrite(t, filepath.Join(source, "skills", "extra", "SKILL.md"), "version one")
	mustWrite(t, filepath.Join(source, "skills", "README.md"), "package skill documentation")
	mustWrite(t, filepath.Join(source, "CLAUDE.md"), "package operating instructions")
	mustWrite(t, filepath.Join(source, ".claude", "settings.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"printf hook-ok"}]}]}}`)
	mustWrite(t, filepath.Join(source, ".codex", "hooks.json"), `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"printf codex-ok"}]}]}}`)
	const hostEnv = "KEEP=operator\n"
	mustWrite(t, filepath.Join(dataDir, ".env"), hostEnv)
	cmdPackages([]string{"minimal", "install", source, "--group", "main"})
	if data, err := os.ReadFile(filepath.Join(dataDir, ".env")); err != nil || string(data) != hostEnv {
		t.Fatalf("skills-only package changed host env: %q, %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(dataDir, "groups", "main", "CLAUDE.md")); err != nil || string(data) != "custom group runbook" {
		t.Fatalf("group runbook changed: %q %v", data, err)
	}
	if names, err := database.SelectedPackageNames("other"); err != nil || !slices.Equal(names, []string{"core"}) {
		t.Fatalf("other group sees %v: %v", names, err)
	}
	names, err := database.SelectedPackageNames("main")
	if err != nil || !slices.Equal(names, []string{"core", "addon"}) {
		t.Fatalf("selected %v: %v", names, err)
	}
	if got := selectedSkill(t, dataDir, "main", "addon", "extra"); got != "version one" {
		t.Fatalf("installed skill %q", got)
	}
	output, err := exec.Command(filepath.Join(dataDir, "packages", "addon", "bin", "demo")).Output()
	if err != nil || string(output) != "tool-ok" {
		t.Fatalf("installed executable: %q %v", output, err)
	}
	mustWrite(t, filepath.Join(source, "skills", "extra", "SKILL.md"), "version two")
	cmdPackages([]string{"minimal", "upgrade", "addon", "--group", "main"})
	if got := selectedSkill(t, dataDir, "main", "addon", "extra"); got != "version two" {
		t.Fatalf("updated skill %q", got)
	}
	cmdPackages([]string{"minimal", "remove", "addon", "--group", "main"})
	cmdPackages([]string{"minimal", "sync", "--group", "main"})
	if _, found, err := db.InstalledPackage("main", "addon"); err != nil || found {
		t.Fatalf("removed package returned: found=%v err=%v", found, err)
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "groups", "main", container.ManifestFile))
	if err != nil || strings.Contains(string(data), source) {
		t.Fatalf("removed source in manifest: %q %v", data, err)
	}
}
