package ipc

// The gated socket is reachable from a container script through apify's `mcpc`,
// and the shipped skills carry the invocation an agent copies. That invocation
// is a contract with a client this repo does not pin (`ant/Dockerfile` installs
// `@apify/mcpc` unpinned), so the form can rot under the fleet with no code
// change — and it did: three skills documented `mcpc connect "socat
// UNIX-CONNECT:$ARIZUKO_MCP_SOCKET -"`, which the client rejects because
// `connect` takes a URL or a `<config-file>:<entry>` pair and never a command.
// The argument was read as a config-file path, so every ad-hoc MCP call from a
// script failed, the release-announce step in `ant/skills/migrate` included
// (BUGS M1).
//
// This pins the one property that separates the two grammars: the server
// argument is a single token. A command form always carries a space.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// mcpcConnect captures the server argument of an `mcpc connect` invocation,
// quoted or bare.
var mcpcConnect = regexp.MustCompile(`mcpc connect\s+(?:"([^"]*)"|'([^']*)'|(\S+))`)

// TestSkillsDocumentAConnectableMcpcForm walks the shipped skills and fails on
// any documented `mcpc connect` whose server argument is a command.
//
// `ant/skills/self/migrations/` is skipped: a migration summary is a frozen
// record of what shipped that day, and rewriting one would falsify history.
func TestSkillsDocumentAConnectableMcpcForm(t *testing.T) {
	root := filepath.Join("..", "ant", "skills")
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("stat %s: %v — the skill tree is the subject of this test, not an optional fixture", root, err)
	}

	frozen := filepath.Join(root, "self", "migrations")
	checked := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == frozen {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".md") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range mcpcConnect.FindAllStringSubmatch(string(b), -1) {
			arg := m[1] + m[2] + m[3]
			checked++
			if strings.ContainsAny(arg, " \t") {
				t.Errorf("%s documents `mcpc connect %q`: the server argument is a command, "+
					"and mcpc accepts only a URL or a <config-file>:<entry> pair. Declare the "+
					"socat bridge as a stdio entry in a config file and connect that entry.",
					path, arg)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if checked == 0 {
		t.Fatal("no `mcpc connect` invocation found in the shipped skills — " +
			"this test guards a form that must still be documented somewhere")
	}
}
