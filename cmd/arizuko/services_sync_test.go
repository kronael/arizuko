package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedSyncInstance builds an instance whose services/ holds installed copies and
// whose HOST_APP_DIR points at a catalog — the shape packageTemplates resolves.
func seedSyncInstance(t *testing.T, name string, catalog, installed map[string]string) string {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ARIZUKO_DATA_DIR", base)
	dataDir := filepath.Join(base, "arizuko_"+name)
	tmplDir := filepath.Join(base, "app", "template", "services")
	svcDir := filepath.Join(dataDir, "services")
	for dir, files := range map[string]map[string]string{tmplDir: catalog, svcDir: installed} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, body := range files {
			if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	env := "HOST_APP_DIR=" + filepath.Join(base, "app") + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	// godotenv.Load (core.LoadConfigFrom, inside packageTemplates) never
	// overrides an already-set process env var, so an earlier test's
	// HOST_APP_DIR would otherwise leak into this one. t.Setenv both pins the
	// correct value up front and restores it when this test ends.
	t.Setenv("HOST_APP_DIR", filepath.Join(base, "app"))
	return dataDir
}

const catalogTeled = "services:\n  teled:\n    image: arizuko:latest\n" +
	"    volumes:\n      - '${DATA_DIR}/store/teled:/srv/app/home/store/teled'\n"

// R1: the E1 mount fix was inert because nothing ever refreshed an instance's
// installed fragment. relinkCatalog is the replacement — a byte-identical copy
// becomes a symlink at the catalog on every generate, and a multi-account
// variant is never touched (rewriting teled-rhias.yml with the base template
// would give two services one container_name).
func TestRelinkCatalogPointsIdenticalCopyAtCatalogAndSparesVariant(t *testing.T) {
	variant := "services:\n  teled-rhias:\n    image: arizuko:latest\n"
	dataDir := seedSyncInstance(t, "s",
		map[string]string{"teled.yml": catalogTeled},
		map[string]string{"teled.yml": catalogTeled, "teled-rhias.yml": variant})

	relinkCatalog(dataDir)

	svcDir := filepath.Join(dataDir, "services")
	fi, err := os.Lstat(filepath.Join(svcDir, "teled.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("teled.yml was not converted to a symlink")
	}
	if got := readSvc(t, svcDir, "teled.yml"); !strings.Contains(got, "store/teled") {
		t.Errorf("teled.yml does not read through to the catalog content: %q", got)
	}
	if v := readSvc(t, svcDir, "teled-rhias.yml"); v != variant {
		t.Errorf("variant rewritten: %q", v)
	}
	if fi, err := os.Lstat(filepath.Join(svcDir, "teled-rhias.yml")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Error("variant was converted to a symlink")
	}
}

// A hand-edited copy — content that differs from the catalog — is never
// touched: converting it would silently discard the operator's edit.
func TestRelinkCatalogSparesHandEditedCopy(t *testing.T) {
	edited := "services:\n  teled:\n    image: arizuko:latest\n    ports: ['1234:1234']\n"
	dataDir := seedSyncInstance(t, "e",
		map[string]string{"teled.yml": catalogTeled},
		map[string]string{"teled.yml": edited})

	relinkCatalog(dataDir)

	svcDir := filepath.Join(dataDir, "services")
	if fi, err := os.Lstat(filepath.Join(svcDir, "teled.yml")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("hand-edited copy was converted to a symlink")
	}
	if got := readSvc(t, svcDir, "teled.yml"); got != edited {
		t.Errorf("hand-edited copy was rewritten: %q", got)
	}
}

// Without HOST_APP_DIR set, relinkCatalog must do nothing rather than link to
// a path derived from this process's own working directory (CLAUDE.md
// "identity is configured, never derived").
func TestRelinkCatalogNoopWithoutHostAppDir(t *testing.T) {
	dataDir := seedSyncInstance(t, "n",
		map[string]string{"teled.yml": catalogTeled},
		map[string]string{"teled.yml": catalogTeled})
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte("ASSISTANT_NAME=x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	relinkCatalog(dataDir)

	svcDir := filepath.Join(dataDir, "services")
	if fi, err := os.Lstat(filepath.Join(svcDir, "teled.yml")); err != nil || fi.Mode()&os.ModeSymlink != 0 {
		t.Fatal("linked without HOST_APP_DIR configured")
	}
}

// A second relink is a no-op in effect: already-linked, re-linked to the same
// target, nothing rewritten that a diff would show.
func TestRelinkCatalogIdempotentAcrossGenerates(t *testing.T) {
	dataDir := seedSyncInstance(t, "i",
		map[string]string{"teled.yml": catalogTeled},
		map[string]string{"teled.yml": catalogTeled})

	relinkCatalog(dataDir)
	relinkCatalog(dataDir)

	svcDir := filepath.Join(dataDir, "services")
	fi, err := os.Lstat(filepath.Join(svcDir, "teled.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatal("teled.yml is not a symlink after two relinks")
	}
}

func readSvc(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
