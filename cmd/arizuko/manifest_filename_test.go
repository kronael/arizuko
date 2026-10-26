package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestManifestApplyAndSyncPreserveOwnership(t *testing.T) {
	dataDir, base := mixInstance(t, map[string]map[string]string{
		"one": {"arizuko.yml": "name: one\n", "PERSONA.md": "first", "skills/first/SKILL.md": "first skill"},
		"two": {"arizuko.yml": "name: two\n", "skills/second/SKILL.md": "second skill"},
	}, []string{"one", "two"})
	path := filepath.Join(dataDir, "groups", "main", "arizuko.yml")
	selection, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	body := "# operator metadata\nname: caller\nbrand: Mine\n" + string(selection) +
		"\nenv:\n  NEWS_FEED_TOKEN: {required: true, hint: Encrypted group secret}\n"
	mustWrite(t, path, body)
	cmdProducts([]string{"t", "apply", "main"})
	mustWrite(t, filepath.Join(base, "src", "two", "skills", "second", "SKILL.md"), "updated skill")
	cmdPackages([]string{"t", "sync", "--group", "main"})
	if got, err := os.ReadFile(path); err != nil || string(got) != body {
		t.Fatalf("group manifest changed: %q %v", got, err)
	}
	if got := selectedSkill(t, dataDir, "main", "two", "second"); got != "updated skill" {
		t.Fatalf("package did not update: %q", got)
	}
}

func TestManifestRequiredForSources(t *testing.T) {
	source := t.TempDir()
	if _, err := preparePackageCandidate(source, source, "local"); err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
		t.Fatalf("manifestless source accepted: %v", err)
	}
	mustWrite(t, filepath.Join(source, "arizuko.yml"), "brand: nameless\n")
	if _, err := preparePackageCandidate(source, source, "local"); err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
		t.Fatalf("nameless source accepted: %v", err)
	}
}
