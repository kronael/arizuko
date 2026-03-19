package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadRecentEpisodes_Empty(t *testing.T) {
	dir := t.TempDir()
	if got := ReadRecentEpisodes(dir); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestReadRecentEpisodes_NoDir(t *testing.T) {
	if got := ReadRecentEpisodes("/nonexistent/path"); got != "" {
		t.Errorf("expected empty for missing dir, got %q", got)
	}
}

func TestReadRecentEpisodes_WithFiles(t *testing.T) {
	dir := t.TempDir()
	eps := filepath.Join(dir, "episodes")
	os.MkdirAll(eps, 0o755)

	// write two day episodes and one week episode
	for _, f := range []struct{ name, content string }{
		{"20260314.md", "---\nsummary: day one\ntype: day\n---\n"},
		{"20260315.md", "---\nsummary: day two\ntype: day\n---\n"},
		{"2026-W11.md", "---\nsummary: week summary\ntype: week\n---\n"},
	} {
		os.WriteFile(filepath.Join(eps, f.name), []byte(f.content), 0o644)
	}

	got := ReadRecentEpisodes(dir)
	if got == "" {
		t.Fatal("expected non-empty result")
	}
	if !strings.Contains(got, "day two") {
		t.Errorf("expected most recent day episode, got %q", got)
	}
	if !strings.Contains(got, "week summary") {
		t.Errorf("expected week episode, got %q", got)
	}
	if !strings.Contains(got, `<episodes`) {
		t.Errorf("expected XML episodes tag, got %q", got)
	}
}

func TestReadRecentEpisodes_MalformedFrontmatter(t *testing.T) {
	dir := t.TempDir()
	eps := filepath.Join(dir, "episodes")
	os.MkdirAll(eps, 0o755)
	// bad file + good file
	os.WriteFile(filepath.Join(eps, "bad.md"), []byte("no frontmatter"), 0o644)
	os.WriteFile(filepath.Join(eps, "good.md"), []byte("---\nsummary: ok\ntype: day\n---\n"), 0o644)

	got := ReadRecentEpisodes(dir)
	if !strings.Contains(got, "ok") {
		t.Errorf("expected good file in result, got %q", got)
	}
}
