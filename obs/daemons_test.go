package obs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Root CLAUDE.md ("### Observability") and specs/5/O §Surface both state the
// invariant as universal — "every daemon calls defer obs.Setup(...)" — and
// nothing enforced it, so ttsd shipped without the call and the drift was only
// found by reading every main() by hand. This test is the enforcement.
//
// "Daemon" is the repo's own naming rule (root CLAUDE.md "Service
// Architecture": daemons end in `d`, libraries don't): a top-level directory
// whose name ends in `d` and holds a Go `package main`, either directly or
// under cmd/<name>/. Directories with no Go main are the TypeScript adapters
// (whapd, twitd) and the image-only services (davd, vited) — they have no
// main() to wire. `crackbox` is out by the same rule and by design: it is a
// shippable standalone component whose orthogonality is the import-graph rule
// (root CLAUDE.md "Canonical paths" — no arizuko-internal subpackage imports),
// and obs is arizuko-internal.
func TestEveryDaemonWiresSetup(t *testing.T) {
	mains := daemonMains(t)
	if len(mains) < 15 {
		t.Fatalf("found %d daemon mains, expected the full set — the walk is broken", len(mains))
	}
	for daemon, path := range mains {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		body := string(src)
		// chanlib.Run calls obs.Setup for every channel adapter (chanlib/run.go),
		// so going through it satisfies the invariant without a second call site.
		if !strings.Contains(body, "obs.Setup(") && !strings.Contains(body, "chanlib.Run(") {
			t.Errorf("%s (%s) wires neither obs.Setup nor chanlib.Run", daemon, path)
		}
	}
}

// daemonMains maps daemon name -> path of its package-main file, for every
// top-level directory whose name ends in `d`. "cmd" is the CLI tree, not a
// daemon, despite ending in the letter.
func daemonMains(t *testing.T) map[string]string {
	t.Helper()
	const repo = ".."
	entries, err := os.ReadDir(repo)
	if err != nil {
		t.Fatalf("read repo root: %v", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() || name == "cmd" || !strings.HasSuffix(name, "d") {
			continue
		}
		direct := filepath.Join(repo, name, "main.go")
		if _, err := os.Stat(direct); err == nil {
			out[name] = direct
			continue
		}
		nested := filepath.Join(repo, name, "cmd", name, "main.go")
		if _, err := os.Stat(nested); err == nil {
			out[name] = nested
		}
	}
	return out
}
