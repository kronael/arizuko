package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestMemoryParentSymlinkConfined(t *testing.T) {
	d, folder, mux := setupNestedMemoryTest(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "PERSONA.md")
	if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	diary := filepath.Join(d.groupsDir, folder, "diary")
	if err := os.Remove(diary); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, diary); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, authMemReq("PUT", "/dash/memory/"+folder+"/diary/PERSONA.md", "overwrite"))
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("outside file=%q HTTP=%d", got, w.Code)
	}
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, authMemReq("DELETE", "/dash/memory/"+folder+"/diary/PERSONA.md", ""))
	got, err = os.ReadFile(victim)
	if err != nil || string(got) != "original" {
		t.Fatalf("outside file=%q error=%v HTTP=%d", got, err, w.Code)
	}
}

func TestMemoryFixedTempSymlinkIgnored(t *testing.T) {
	d, folder, mux := setupNestedMemoryTest(t)
	outside := t.TempDir()
	victim := filepath.Join(outside, "PERSONA.md")
	if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(d.groupsDir, folder, "MEMORY.md.tmp")); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, authMemReq("PUT", "/dash/memory/"+folder+"/MEMORY.md", "overwrite"))
	got, err := os.ReadFile(victim)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "original" {
		t.Fatalf("outside file=%q HTTP=%d", got, w.Code)
	}
	if w.Code != 204 {
		t.Fatalf("HTTP=%d", w.Code)
	}
	got, err = os.ReadFile(filepath.Join(d.groupsDir, folder, "MEMORY.md"))
	if err != nil || string(got) != "overwrite" {
		t.Fatalf("memory file=%q error=%v", got, err)
	}
}
