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
	return dataDir
}

const catalogTeled = "services:\n  teled:\n    image: arizuko:latest\n" +
	"    volumes:\n      - '${DATA_DIR}/store/teled:/srv/app/home/store/teled'\n"

// R1: the E1 mount fix was inert because nothing ever refreshes an instance's
// installed fragment. --sync-services is that path — and it must never destroy
// what it replaces, nor touch a multi-account variant (rewriting teled-rhias.yml
// with the base template would give two services one container_name).
func TestSyncFragmentsUpdatesCopyBacksUpAndSparesVariant(t *testing.T) {
	stale := "services:\n  teled:\n    image: arizuko:latest\n"
	variant := "services:\n  teled-rhias:\n    image: arizuko:latest\n"
	dataDir := seedSyncInstance(t, "s",
		map[string]string{"teled.yml": catalogTeled},
		map[string]string{"teled.yml": stale, "teled-rhias.yml": variant})

	syncFragments(dataDir)

	svcDir := filepath.Join(dataDir, "services")
	got := readSvc(t, svcDir, "teled.yml")
	if !strings.Contains(got, "store/teled") {
		t.Errorf("teled.yml not updated from the catalog: %q", got)
	}
	if bak := readSvc(t, svcDir, "teled.yml.bak"); bak != stale {
		t.Errorf("previous content not preserved: %q", bak)
	}
	if v := readSvc(t, svcDir, "teled-rhias.yml"); v != variant {
		t.Errorf("variant rewritten: %q", v)
	}
}

// A second sync is a no-op: the fragment now matches the catalog, so nothing is
// rewritten and no stale .bak is minted on every deploy.
func TestSyncFragmentsIdempotent(t *testing.T) {
	dataDir := seedSyncInstance(t, "i",
		map[string]string{"teled.yml": catalogTeled},
		map[string]string{"teled.yml": catalogTeled})

	syncFragments(dataDir)

	if _, err := os.Stat(filepath.Join(dataDir, "services", "teled.yml.bak")); !os.IsNotExist(err) {
		t.Fatalf("current fragment backed up anyway: %v", err)
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
