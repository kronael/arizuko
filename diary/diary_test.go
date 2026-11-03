package diary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractSummary(t *testing.T) {
	dir := t.TempDir()

	// With quotes
	p1 := filepath.Join(dir, "a.md")
	os.WriteFile(p1, []byte("---\nsummary: \"hello world\"\n---\nbody"), 0o644)
	if got := ExtractSummary(p1); got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}

	// Without quotes
	p2 := filepath.Join(dir, "b.md")
	os.WriteFile(p2, []byte("---\nsummary: plain text\n---\nbody"), 0o644)
	if got := ExtractSummary(p2); got != "plain text" {
		t.Fatalf("expected 'plain text', got %q", got)
	}

	// No summary field
	p3 := filepath.Join(dir, "c.md")
	os.WriteFile(p3, []byte("---\ntitle: test\n---\nbody"), 0o644)
	if got := ExtractSummary(p3); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	// No frontmatter
	p4 := filepath.Join(dir, "d.md")
	os.WriteFile(p4, []byte("just text"), 0o644)
	if got := ExtractSummary(p4); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	// Nonexistent file
	if got := ExtractSummary(filepath.Join(dir, "missing.md")); got != "" {
		t.Fatalf("expected empty for missing file, got %q", got)
	}

	// Block scalar with | (literal)
	p5 := filepath.Join(dir, "e.md")
	os.WriteFile(p5, []byte("---\nsummary: |\n  line one\n  line two\n---\nbody"), 0o644)
	got := ExtractSummary(p5)
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Fatalf("expected block scalar lines, got %q", got)
	}

	// Block scalar with > (folded)
	p6 := filepath.Join(dir, "f.md")
	os.WriteFile(p6, []byte("---\nsummary: >\n  - item a\n  - item b\n---\nbody"), 0o644)
	got = ExtractSummary(p6)
	if !strings.Contains(got, "item a") || !strings.Contains(got, "item b") {
		t.Fatalf("expected folded block scalar lines, got %q", got)
	}
}
