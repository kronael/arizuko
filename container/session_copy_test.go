package container

import (
	"bytes"
	"log/slog"
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

// BUGS L920: a parent topic can run while a child forks from it, so its
// transcript can end in half a line. The copy keeps only whole lines.
func TestCopySession_DropsAPartialLastLine(t *testing.T) {
	dir := t.TempDir()
	projDir := filepath.Join(dir, ".claude", "projects", "-home-node")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	whole := `{"type":"user","sessionId":"p"}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "p.jsonl"), []byte(whole+`{"type":"assis`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := CopySession(dir, "p", "c"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(projDir, "c.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"type":"user","sessionId":"c"}` + "\n"; string(got) != want {
		t.Fatalf("child transcript %q, want %q", got, want)
	}
}

func TestCopySession_CodexRolloutSkipsFork(t *testing.T) {
	dir := t.TempDir()
	rolloutDir := filepath.Join(dir, ".codex", "sessions", "2026", "11", "09")
	if err := os.MkdirAll(rolloutDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rolloutDir, "rollout-2026-11-09T12-00-00-parent-uuid.jsonl"), []byte("parent\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	copied, err := CopySession(dir, "parent-uuid", "child-uuid")
	if err != nil || copied {
		t.Errorf("CopySession = %v, %v; want false, nil", copied, err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".claude", "projects", "-home-node", "child-uuid.jsonl")); !os.IsNotExist(err) {
		t.Errorf("child transcript exists: %v", err)
	}
	if got := logs.String(); !strings.Contains(got, "level=INFO") || !strings.Contains(got, "Codex") || strings.Contains(got, "level=WARN") {
		t.Errorf("want Codex INFO without warning, got %s", got)
	}
}

// pi keeps a session at .pi/sessions/<timestamp>_<id>.jsonl and resolves
// `--session <id>` by the id in the file's first line. A fork of a pi parent
// is that file under the child id, with only the header restamped, so the
// child's first turn resumes the parent's history.
func TestCopySession_PiSessionCopiedUnderChildID(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, ".pi", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	const src = "01a1f04d-2baa-730e-a44a-51b102c383f3"
	const dst = "dcac785c-1e7b-4fa2-83aa-69dc72ecdb2f"
	header := `{"type":"session","version":3,"id":"` + src + `","timestamp":"2026-11-18T20:15:51.724Z","cwd":"/home/node"}`
	entries := `{"type":"model_change","id":"26567967","parentId":null,"provider":"openai-codex","modelId":"m"}` + "\n" +
		`{"type":"custom","id":"` + src + `","parentId":"26567967"}` + "\n"
	parent := filepath.Join(sessions, "2026-11-18T20-15-51-724Z_"+src+".jsonl")
	if err := os.WriteFile(parent, []byte(header+"\n"+entries), 0o644); err != nil {
		t.Fatal(err)
	}

	copied, err := CopySession(dir, src, dst)
	if err != nil || !copied {
		t.Fatalf("CopySession = %v, %v; want true, nil", copied, err)
	}
	children, err := filepath.Glob(filepath.Join(sessions, "*_"+dst+".jsonl"))
	if err != nil || len(children) != 1 {
		t.Fatalf("child sessions = %v, %v; want one file", children, err)
	}
	got, err := os.ReadFile(children[0])
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Replace(header, src, dst, 1) + "\n" + entries
	if string(got) != want {
		t.Errorf("child session\nwant: %q\n got: %q", want, got)
	}
	kept, err := os.ReadFile(parent)
	if err != nil || string(kept) != header+"\n"+entries {
		t.Errorf("parent session changed: %q, %v", kept, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(sessions, "*.tmp"))
	if err != nil || len(leftovers) != 0 {
		t.Errorf("tmp files lingered: %v, %v", leftovers, err)
	}
}

// A pi file whose header does not carry the parent id cannot be restamped:
// the copy would resolve under no id. That is an error, never a silent copy,
// because routd keeps the child's observed cursor only for a usable copy.
func TestCopySession_PiHeaderWithoutParentIDIsRefused(t *testing.T) {
	dir := t.TempDir()
	sessions := filepath.Join(dir, ".pi", "sessions")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"type":"session","version":3,"id":"another-id"}` + "\n"
	if err := os.WriteFile(filepath.Join(sessions, "2026-11-18T20-15-51-724Z_parent-uuid.jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	copied, err := CopySession(dir, "parent-uuid", "child-uuid")
	if err == nil || copied {
		t.Errorf("CopySession = %v, %v; want false and an error", copied, err)
	}
	children, globErr := filepath.Glob(filepath.Join(sessions, "*_child-uuid.jsonl*"))
	if globErr != nil || len(children) != 0 {
		t.Errorf("child session written: %v, %v", children, globErr)
	}
}
