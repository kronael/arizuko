package gateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonaBlock_NoFile(t *testing.T) {
	gw, _ := testGateway(t)
	if got := gw.personaBlock("nogroup"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestPersonaBlock_NoFrontmatter(t *testing.T) {
	gw, _ := testGateway(t)
	dir := filepath.Join(gw.cfg.GroupsDir, "g1")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "PERSONA.md"),
		[]byte("# Persona\nno frontmatter at all\n"), 0o644)

	if got := gw.personaBlock("g1"); got != "" {
		t.Errorf("expected empty (no frontmatter), got %q", got)
	}
}

func TestPersonaBlock_NoSummary(t *testing.T) {
	gw, _ := testGateway(t)
	dir := filepath.Join(gw.cfg.GroupsDir, "g1")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "PERSONA.md"),
		[]byte("---\nname: Atlas\n---\n# body\n"), 0o644)

	if got := gw.personaBlock("g1"); got != "" {
		t.Errorf("expected empty (no summary field), got %q", got)
	}
}

func TestPersonaBlock_WithSummary(t *testing.T) {
	gw, _ := testGateway(t)
	dir := filepath.Join(gw.cfg.GroupsDir, "atlas")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "PERSONA.md"),
		[]byte(`---
name: Atlas
summary: |
  Codebase-native marinade guide. Dry, punchy.
  Cites paths. Refuses to guess.
---
# body
`), 0o644)

	got := gw.personaBlock("atlas")
	if !strings.Contains(got, `<persona name="Atlas">`) {
		t.Errorf("missing opening tag: %q", got)
	}
	if !strings.Contains(got, "Codebase-native marinade guide") {
		t.Errorf("missing summary content: %q", got)
	}
	if !strings.Contains(got, "(For full register: /persona)") {
		t.Errorf("missing pointer trailer: %q", got)
	}
	if !strings.HasSuffix(got, "</persona>\n") {
		t.Errorf("missing closing tag: %q", got)
	}
}
