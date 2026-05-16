package container

import (
	"os"
	"path/filepath"
	"testing"
)

// Spec 6/F: CopySession duplicates the parent Claude Code session jsonl
// to a fresh uuid under the same projects dir. Pure file op; the
// container path layout (~/.claude/projects/-home-node/<uuid>.jsonl)
// is enforced by the slug constant.
func TestCopySession_DuplicatesBytes(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".claude", "projects", "-home-node")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	srcUUID := "parent-uuid"
	dstUUID := "child-uuid"
	want := []byte(`{"type":"summary","summary":"x"}` + "\n" +
		`{"type":"user","message":{"role":"user","content":"hi"}}` + "\n")
	if err := os.WriteFile(filepath.Join(projDir, srcUUID+".jsonl"), want, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := CopySession(dir, srcUUID, dstUUID); err != nil {
		t.Fatalf("CopySession: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(projDir, dstUUID+".jsonl"))
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("dst content drift\nwant: %q\n got: %q", want, got)
	}
	// Temp file must be cleaned up.
	if _, err := os.Stat(filepath.Join(projDir, dstUUID+".jsonl.tmp")); !os.IsNotExist(err) {
		t.Errorf("tmp file lingered: %v", err)
	}
}

// Missing parent file = graceful no-op (returns nil). Child starts
// fresh; gateway logs WARN. Without this, every brand-new folder's
// first non-main topic would error because main has no session yet.
func TestCopySession_MissingParentReturnsNil(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects", "-home-node"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CopySession(dir, "absent-uuid", "child-uuid"); err != nil {
		t.Errorf("CopySession with missing parent: %v, want nil", err)
	}
	// And no child file was created.
	dst := filepath.Join(dir, ".claude", "projects", "-home-node", "child-uuid.jsonl")
	if _, err := os.Stat(dst); !os.IsNotExist(err) {
		t.Errorf("dst created from missing parent: %v", err)
	}
}

// Projects dir may not exist yet on a brand-new folder. CopySession
// must mkdir before writing. (Skip when src missing — that's a no-op.)
func TestCopySession_CreatesProjDirOnDemand(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".claude", "projects", "-home-node")
	// Set up src in a sibling dir, then remove dst structure.
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "p.jsonl"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// CopySession to the same projDir — already exists, just confirms
	// MkdirAll is idempotent.
	if err := CopySession(dir, "p", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projDir, "c.jsonl")); err != nil {
		t.Errorf("child not created: %v", err)
	}
}
