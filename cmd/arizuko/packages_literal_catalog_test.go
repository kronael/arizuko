package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackagesInstallLiteralCatalogSource(t *testing.T) {
	pkgChildRun()
	dataDir := seedSyncInstance(t, "source-catalog", map[string]string{}, map[string]string{})
	seedRoutdDB(t, dataDir)
	source := writePkg(t, filepath.Dir(dataDir), "catalog", "")
	t.Chdir(filepath.Dir(dataDir))
	cmdPackages([]string{"source-catalog", "install", "catalog"})
	rec := mustRec(t, dataDir, "catalog")
	if rec.Source != source || !filepath.IsAbs(rec.Source) {
		t.Fatalf("local source must retain absolute identity: %+v", rec)
	}
	fragment := filepath.Join(source, "catalog.yml")
	if err := os.WriteFile(fragment, []byte("services:\n  catalog:\n    image: updated\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(t.TempDir())
	cmdPackages([]string{"source-catalog", "upgrade", "catalog"})
	installed := filepath.Join(dataDir, "services", "catalog.yml")
	content, err := os.ReadFile(installed)
	if err != nil || !strings.Contains(string(content), "image: updated") {
		t.Fatalf("absolute local source did not upgrade from another directory: %q %v", content, err)
	}
	if err := os.WriteFile(fragment, []byte("services:\n  routd:\n    image: hostile\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runPkgExpectingDeath(t, []string{"source-catalog", "upgrade", "catalog"}); code == 0 {
		t.Fatal("local catalog source bypassed sourced upgrade core-service check")
	}
	content, err = os.ReadFile(installed)
	if err != nil || !strings.Contains(string(content), "image: updated") {
		t.Fatalf("refused upgrade changed installed fragment: %q %v", content, err)
	}
}
