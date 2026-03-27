package diary

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeDiary(t *testing.T, dir, name, content string) {
	t.Helper()
	d := filepath.Join(dir, "diary")
	os.MkdirAll(d, 0o755)
	os.WriteFile(filepath.Join(d, name), []byte(content), 0o644)
}

func TestReadEmpty(t *testing.T) {
	dir := t.TempDir()
	got := Read(dir, 5)
	if got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}

func TestReadNoDiary(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "diary"), 0o755)
	got := Read(dir, 5)
	if got != "" {
		t.Fatalf("expected empty for no .md files, got %q", got)
	}
}

func TestReadWithSummary(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("20060102")
	writeDiary(t, dir, today+".md", `---
summary: "deployed arizuko v0.0.2"
---
# Details
stuff happened
`)

	got := Read(dir, 5)
	if !strings.Contains(got, "deployed arizuko v0.0.2") {
		t.Fatalf("expected summary in output, got %q", got)
	}
	if !strings.Contains(got, `age="today"`) {
		t.Fatalf("expected age=today, got %q", got)
	}
}

func TestReadMaxLimit(t *testing.T) {
	dir := t.TempDir()
	for i := 1; i <= 5; i++ {
		d := time.Now().AddDate(0, 0, -i).Format("20060102")
		writeDiary(t, dir, d+".md", `---
summary: "day `+d+`"
---
`)
	}

	got := Read(dir, 2)
	count := strings.Count(got, "<entry")
	if count != 2 {
		t.Fatalf("expected 2 entries (max=2), got %d", count)
	}
}

func TestReadSkipsNoSummary(t *testing.T) {
	dir := t.TempDir()
	today := time.Now().Format("20060102")
	writeDiary(t, dir, today+".md", "# No frontmatter\njust text")

	got := Read(dir, 5)
	if strings.Contains(got, "<entry") {
		t.Fatal("should skip entries without summary")
	}
}

func TestAgeLabel(t *testing.T) {
	now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
	today := "20260307"
	yesterday := "20260306"

	cases := []struct {
		key  string
		want string
	}{
		{"20260307", "today"},
		{"20260306", "yesterday"},
		{"20260304", "3 days ago"},
		{"20260228", "last week"},
		{"20260221", "2 weeks ago"},
		{"baddate", "unknown"},
	}
	for _, c := range cases {
		got := ageLabel(c.key, today, yesterday, now)
		if got != c.want {
			t.Errorf("ageLabel(%q) = %q, want %q", c.key, got, c.want)
		}
	}
}

func TestExtractSummary(t *testing.T) {
	dir := t.TempDir()

	// With quotes
	p1 := filepath.Join(dir, "a.md")
	os.WriteFile(p1, []byte("---\nsummary: \"hello world\"\n---\nbody"), 0o644)
	if got := extractSummary(p1); got != "hello world" {
		t.Fatalf("expected 'hello world', got %q", got)
	}

	// Without quotes
	p2 := filepath.Join(dir, "b.md")
	os.WriteFile(p2, []byte("---\nsummary: plain text\n---\nbody"), 0o644)
	if got := extractSummary(p2); got != "plain text" {
		t.Fatalf("expected 'plain text', got %q", got)
	}

	// No summary field
	p3 := filepath.Join(dir, "c.md")
	os.WriteFile(p3, []byte("---\ntitle: test\n---\nbody"), 0o644)
	if got := extractSummary(p3); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	// No frontmatter
	p4 := filepath.Join(dir, "d.md")
	os.WriteFile(p4, []byte("just text"), 0o644)
	if got := extractSummary(p4); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}

	// Nonexistent file
	if got := extractSummary(filepath.Join(dir, "missing.md")); got != "" {
		t.Fatalf("expected empty for missing file, got %q", got)
	}

	// Block scalar with | (literal)
	p5 := filepath.Join(dir, "e.md")
	os.WriteFile(p5, []byte("---\nsummary: |\n  line one\n  line two\n---\nbody"), 0o644)
	got := extractSummary(p5)
	if !strings.Contains(got, "line one") || !strings.Contains(got, "line two") {
		t.Fatalf("expected block scalar lines, got %q", got)
	}

	// Block scalar with > (folded)
	p6 := filepath.Join(dir, "f.md")
	os.WriteFile(p6, []byte("---\nsummary: >\n  - item a\n  - item b\n---\nbody"), 0o644)
	got = extractSummary(p6)
	if !strings.Contains(got, "item a") || !strings.Contains(got, "item b") {
		t.Fatalf("expected folded block scalar lines, got %q", got)
	}
}
