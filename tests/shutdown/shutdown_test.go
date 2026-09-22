// Package shutdown checks that every daemon arms its signal handler before it
// starts accepting connections.
//
// Between the listener accepting and signal.Notify returning, a SIGTERM takes
// Go's default disposition: the process dies and the srv.Shutdown written
// below it never runs. Six of nine daemons had that window (BUGS L127), and
// one of them failed its own main() test with "signal: terminated". The
// ordering is a source fact, so this test reads it from the source — the same
// way the defect was measured.
//
// THE SET IS DISCOVERED, NOT LISTED. It was a hand-written list of nine, and
// `timed/split.go` never joined it: timed's server moved into that file and the
// list did not follow, so timed carried the window for as long as the guard
// reported nine green. A guard that names its own scope passes by construction
// for whatever is missing from it.
package shutdown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../.."

// listeners are the calls that start accepting. A new one must be added here,
// or a daemon using it is discovered and then judged as having no listener,
// which fails loudly rather than passing.
var listeners = []string{"ListenAndServe(", "ListenAndServeTLS(", "net.Listen("}

// daemons walks the tree for every main package that starts a listener.
func daemons(t *testing.T) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The walk starts at "../..", whose own name begins with a dot, so
			// the root is never a candidate for skipping.
			if path == root {
				return nil
			}
			// .git and the agent worktrees under .claude hold whole copies of
			// this repo; walking them finds every daemon again, at a stale
			// revision.
			// refs/ and third_party/ are whole copies of other people's
			// projects; their shutdown is not ours to judge.
			switch name := d.Name(); {
			case strings.HasPrefix(name, "."), name == "node_modules",
				name == "refs", name == "third_party":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(b)
		// Half the daemons open with the package clause and half with a doc
		// comment, so the needle must match at byte 0 as well.
		if !strings.HasPrefix(src, "package main\n") && !strings.Contains(src, "\npackage main\n") {
			return nil
		}
		for _, l := range listeners {
			if strings.Contains(src, l) {
				rel, _ := filepath.Rel(root, path)
				out = append(out, filepath.ToSlash(rel))
				return nil
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("found no daemon at all; the walk or the needles are wrong, and an " +
			"empty set is a guard that cannot fail")
	}
	return out
}

// firstLine returns the 1-based line number of the first line holding any of
// needles, or 0 when none does.
func firstLine(lines []string, needles ...string) int {
	for i, l := range lines {
		for _, n := range needles {
			if strings.Contains(l, n) {
				return i + 1
			}
		}
	}
	return 0
}

func TestSignalHandlerIsArmedBeforeTheListener(t *testing.T) {
	found := daemons(t)
	// The nine the hand-written list held. Losing one means the walk stopped
	// reaching it, which would otherwise read as a pass.
	for _, want := range []string{
		"authd/main.go", "dashd/main.go", "egress/cmd/egress/main.go",
		"onbod/main.go", "proxyd/main.go", "routd/cmd/routd/main.go",
		"runed/cmd/runed/main.go", "ttsd/main.go", "webd/main.go",
	} {
		if !contains(found, want) {
			t.Errorf("%s serves HTTP and the walk did not find it", want)
		}
	}

	for _, d := range found {
		b, err := os.ReadFile(filepath.Join(root, d))
		if err != nil {
			t.Errorf("%s: %v", d, err)
			continue
		}
		lines := strings.Split(string(b), "\n")
		notify := firstLine(lines, "signal.Notify(", "signal.NotifyContext(")
		listen := firstLine(lines, listeners...)
		switch {
		case notify == 0:
			t.Errorf("%s: no signal.Notify — a SIGTERM kills it with no graceful shutdown", d)
		case listen == 0:
			t.Errorf("%s: no listener found; update the needles in this test", d)
		case notify > listen:
			t.Errorf("%s: signal.Notify at line %d comes AFTER the listener at line %d; "+
				"a SIGTERM in that window skips Shutdown", d, notify, listen)
		}
	}
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
