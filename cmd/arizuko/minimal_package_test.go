package main

import (
	"os"
	"path/filepath"
	"testing"
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
