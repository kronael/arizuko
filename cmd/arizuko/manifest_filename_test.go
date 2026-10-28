package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
)

func TestManifestRequiredForSources(t *testing.T) {
	source := t.TempDir()
	if _, err := container.ReadProductManifest(source); err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
		t.Fatalf("manifestless source accepted: %v", err)
	}
	mustWrite(t, filepath.Join(source, "arizuko.yml"), "brand: nameless\n")
	if _, err := container.ReadProductManifest(source); err == nil || !strings.Contains(err.Error(), "arizuko.yml") {
		t.Fatalf("nameless source accepted: %v", err)
	}
}
