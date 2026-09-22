package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Spec 5/F-topic-lineage: CopySession duplicates the parent Claude Code session jsonl
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

	copied, err := CopySession(dir, srcUUID, dstUUID)
	if err != nil {
		t.Fatalf("CopySession: %v", err)
	}
	if !copied {
		t.Error("copied = false, want true")
	}
	got, err := os.ReadFile(filepath.Join(projDir, dstUUID+".jsonl"))
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("dst content drift\nwant: %q\n got: %q", want, got)
	}
	if _, err := os.Stat(filepath.Join(projDir, dstUUID+".jsonl.tmp")); !os.IsNotExist(err) {
		t.Errorf("tmp file lingered: %v", err)
	}
}

// Every transcript line carries "sessionId":"<parent>". A copy that keeps the
// parent id is a transcript two topics claim, so the child's own turns append
// to a file whose history names another session. Restamp on copy.
func TestCopySession_RestampsSessionID(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".claude", "projects", "-home-node")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	const src = "a2fb6376-30aa-498c-be8c-c13d58312ab1"
	const dst = "dcac785c-1e7b-4fa2-83aa-69dc72ecdb2f"
	body := `{"type":"user","sessionId":"` + src + `","message":{"role":"user","content":"hi"}}` + "\n" +
		`{"type":"assistant","sessionId":"` + src + `","message":{"role":"assistant"}}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, src+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := CopySession(dir, src, dst); err != nil {
		t.Fatalf("CopySession: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(projDir, dst+".jsonl"))
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if strings.Contains(string(got), src) {
		t.Errorf("child transcript still names the parent session %s:\n%s", src, got)
	}
	if n := strings.Count(string(got), `"sessionId":"`+dst+`"`); n != 2 {
		t.Errorf("restamped %d lines, want 2:\n%s", n, got)
	}
}

// Missing parent file = graceful no-op, reported as copied=false. The caller
// (routd's fork path) reads that to hand the child back its ambient observed
// window — without the signal the child's first turn carries no context at all.
func TestCopySession_MissingParentReportsNotCopied(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".claude", "projects", "-home-node"), 0o755); err != nil {
		t.Fatal(err)
	}
	copied, err := CopySession(dir, "absent-uuid", "child-uuid")
	if err != nil {
		t.Errorf("CopySession with missing parent: %v, want nil", err)
	}
	if copied {
		t.Error("copied = true for a missing parent, want false")
	}
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
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projDir, "p.jsonl"), []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	// CopySession to the same projDir — already exists, just confirms
	// MkdirAll is idempotent.
	if _, err := CopySession(dir, "p", "c"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(projDir, "c.jsonl")); err != nil {
		t.Errorf("child not created: %v", err)
	}
}
