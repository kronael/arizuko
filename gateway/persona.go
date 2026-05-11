package gateway

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var personaFrontmatterRE = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)

// personaBlock reads <groupDir>/PERSONA.md, parses YAML frontmatter,
// and returns a <persona> XML block carrying the `summary` field. The
// block is injected once per inbound turn next to the autocalls block
// so the agent re-anchors its voice register without depending on the
// system-prompt alone (which drifts in multi-turn sessions).
//
// Strict frontmatter: if PERSONA.md is missing, has no frontmatter, or
// has no `summary` field, returns "" — no fallback to body text. The
// agent runs in default register when no persona is configured.
//
// Layered with the on-demand /persona skill (full re-read) and the
// system-prompt body (loaded once at session start). See ant/CLAUDE.md
// "# Persona".
func (g *Gateway) personaBlock(folder string) string {
	if folder == "" {
		return ""
	}
	path := filepath.Join(g.cfg.GroupsDir, folder, "PERSONA.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	fm := personaFrontmatterRE.FindSubmatch(data)
	if fm == nil {
		return ""
	}
	var meta struct {
		Name    string `yaml:"name"`
		Summary string `yaml:"summary"`
	}
	if err := yaml.Unmarshal(fm[1], &meta); err != nil {
		return ""
	}
	if strings.TrimSpace(meta.Summary) == "" {
		return ""
	}
	name := meta.Name
	if name == "" {
		name = folder
	}
	return fmt.Sprintf("<persona name=%q>\n%s\n(For full register: /persona)\n</persona>\n",
		name, strings.TrimSpace(meta.Summary))
}
