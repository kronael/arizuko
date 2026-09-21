package container

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A destination the run uid cannot create is the "typing but no reply" outage:
// the agent spawns without the files this copy was meant to place and answers
// nothing. seedSettings already names that symptom at ERROR; cpDir logged the
// same failure at WARN, which is quieter than the outage it reports. An
// operator filtering on ERROR saw nothing.
func TestCpDirReportsAnUnwritableDestinationAsAnError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A file where the destination directory must go: MkdirAll cannot create it.
	blocked := filepath.Join(t.TempDir(), "home")
	if err := os.WriteFile(blocked, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	cpDirOverwrite(src, filepath.Join(blocked, "skills"))

	got := buf.String()
	if !strings.Contains(got, "level=ERROR") {
		t.Fatalf("an unwritable group home logged no ERROR, so an operator filtering on it sees nothing: %s", got)
	}
	if !strings.Contains(got, "chown") {
		t.Errorf("the log does not say what to do about it: %s", got)
	}
}

// The symlink skip is a deliberate security refusal, not a failure, and must
// stay below ERROR — or the level stops meaning "this broke".
func TestCpDirSymlinkSkipIsNotAnError(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	src, dst := t.TempDir(), t.TempDir()
	if err := os.Symlink("/etc/passwd", filepath.Join(src, "link")); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}
	cpDirOverwrite(src, filepath.Join(dst, "out"))

	if strings.Contains(buf.String(), "level=ERROR") {
		t.Errorf("a skipped symlink was reported as an error: %s", buf.String())
	}
}
