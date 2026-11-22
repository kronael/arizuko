package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSkillMarkerSymlinkDoesNotTruncate(t *testing.T) {
	groups, outside := t.TempDir(), t.TempDir()
	victim := filepath.Join(outside, "PERSONA.md")
	if err := os.WriteFile(victim, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(groups, "alice", ".claude", "skills", "voice", ".disabled")
	if err := os.MkdirAll(filepath.Dir(marker), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, marker); err != nil {
		t.Fatal(err)
	}
	d := &dash{groupsDir: groups}
	err := d.setSkillDisabled("alice", "voice", true)
	got, readErr := os.ReadFile(victim)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "original" {
		t.Fatalf("outside file=%q marker error=%v", got, err)
	}
}
