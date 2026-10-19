// Package resident checks the two always-loaded files against the rule of
// specs/6/34: what left `ant/CLAUDE.md` and root `CLAUDE.md` lives behind a
// pointer, so a pointer that does not resolve routes the reader to nothing —
// and nothing else in the repo would notice. The agent file is seeded into
// every group and rendered into every codex and pi system prompt; a stale
// `self/<file>.md` row there is a rule the agent can no longer find.
package resident

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const root = "../.."

// gatewayCommands are the `/word` tokens routd intercepts before an agent sees
// them (`ant/skills/self/commands.md`). Every other `/word` in the agent file
// names a skill, so it must resolve under ant/skills/.
var gatewayCommands = map[string]bool{
	"new": true, "stop": true, "ping": true, "chatid": true, "root": true,
}

var (
	backtick = regexp.MustCompile("`([^`\n]+)`")
	mdLink   = regexp.MustCompile(`\]\(([^)\s]+\.md)(?:#[^)\s]*)?\)`)
	slashCmd = regexp.MustCompile(`^/([a-z][a-z0-9-]*)(?: .*)?$`)
	mdPath   = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_./-]*\.md$`)
)

func read(t *testing.T, rel string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(body)
}

func exists(rel string) bool {
	_, err := os.Stat(filepath.Join(root, rel))
	return err == nil
}

// TestAgentFilePointersResolve: every `/skill` and every `<skill>/<file>.md`
// the agent file names is a real skill dir or file under ant/skills/, and
// every other `.md` it names exists in the repo. Placeholders (`<…>`) and
// home-relative paths (`~/…`) are the agent's own tree, not this repo's.
func TestAgentFilePointersResolve(t *testing.T) {
	body := read(t, "ant/CLAUDE.md")
	seen := 0
	for _, m := range backtick.FindAllStringSubmatch(body, -1) {
		tok := m[1]
		if strings.ContainsAny(tok, "<~$") {
			continue
		}
		if sm := slashCmd.FindStringSubmatch(tok); sm != nil {
			if gatewayCommands[sm[1]] {
				continue
			}
			seen++
			if !exists(filepath.Join("ant/skills", sm[1], "SKILL.md")) {
				t.Errorf("ant/CLAUDE.md names `%s` but ant/skills/%s/SKILL.md does not exist", tok, sm[1])
			}
			continue
		}
		if mdPath.MatchString(tok) {
			seen++
			if !exists(filepath.Join("ant/skills", tok)) && !exists(tok) {
				t.Errorf("ant/CLAUDE.md names `%s` but neither ant/skills/%s nor %s exists", tok, tok, tok)
			}
		}
	}
	if seen < 10 {
		t.Fatalf("matched only %d pointers in ant/CLAUDE.md; the extractor no longer sees the file's shape", seen)
	}
}

// TestAgentFileCarriesTheCarveOut: the sections spec 6/34 argues cannot leave
// residency — the reader must obey them before it can know to ask. A section
// dropped by a trim is invisible at runtime; here it fails the build.
func TestAgentFileCarriesTheCarveOut(t *testing.T) {
	body := read(t, "ant/CLAUDE.md")
	for _, must := range []string{
		"<messages>", "<reply-to", "<attachment", "<autocalls>", "<topic name=", "<observed>", "<link-context>",
		"A user hears you only through a tool",
		"Text you print is your thinking",
		"verb=mention",
		"AskUserQuestion",
		"role:member",
		"[solve]",
		"<status>",
		"403 on EVERY path",
		"NEVER fabricate",
	} {
		if !strings.Contains(body, must) {
			t.Errorf("ant/CLAUDE.md no longer carries %q — a resident rule left with no pointer", must)
		}
	}
}

// TestMaintainerFilePointersResolve: root CLAUDE.md names repo files by path
// (`INSTALL.md`, `specs/5/8-yaml-manifests.md`, `.claude/skills/eval/SKILL.md`);
// each must exist. Tokens with a placeholder (`<pkg>/README.md`) or a home
// path are skipped.
func TestMaintainerFilePointersResolve(t *testing.T) {
	body := read(t, "CLAUDE.md")
	seen := 0
	check := func(tok string) {
		if strings.ContainsAny(tok, "<~$*") || !mdPath.MatchString(tok) {
			return
		}
		seen++
		if !exists(tok) {
			t.Errorf("CLAUDE.md names `%s` but it does not exist", tok)
		}
	}
	for _, m := range backtick.FindAllStringSubmatch(body, -1) {
		check(m[1])
	}
	for _, m := range mdLink.FindAllStringSubmatch(body, -1) {
		check(m[1])
	}
	if seen < 10 {
		t.Fatalf("matched only %d pointers in CLAUDE.md; the extractor no longer sees the file's shape", seen)
	}
}

// TestMaintainerFileCarriesTheSkillReadSections: three sections of root
// CLAUDE.md are the project half of a global skill — `~/.claude/skills/refine`
// reads the live project file for its lenses, and `~/.claude/skills/release`
// reads it for the steps and the broadcast shape that override its defaults
// (`6/34` rule 4). A project skill cannot hold them: a project skill whose name
// matches a global one is shadowed by it, so the copy would reach no reader.
func TestMaintainerFileCarriesTheSkillReadSections(t *testing.T) {
	body := read(t, "CLAUDE.md")
	for _, must := range []string{
		"Refine scope",
		"Tagging a new version",
		"Announcing",
	} {
		// The heading itself, not a mention of it: root CLAUDE.md quotes
		// "## Announcing" in a cross-reference, and a skill reading the
		// section needs the section.
		if !strings.Contains(body, "\n## "+must+"\n") {
			t.Errorf("CLAUDE.md no longer carries the %q section — a skill reads it from this file", must)
		}
	}
}
