// Package shutdown checks that every daemon arms its signal handler before it
// starts accepting connections.
//
// Between the listener accepting and signal.Notify returning, a SIGTERM takes
// Go's default disposition: the process dies and the srv.Shutdown written
// below it never runs. Six of nine daemons had that window (BUGS L127), and
// one of them failed its own main() test with "signal: terminated". The
// ordering is a source fact, so this test reads it from the source — the same
// way the defect was measured.
package shutdown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../.."

// daemons are every main package that serves HTTP.
var daemons = []string{
	"authd/main.go",
	"dashd/main.go",
	"egress/cmd/egress/main.go",
	"onbod/main.go",
	"proxyd/main.go",
	"routd/cmd/routd/main.go",
	"runed/cmd/runed/main.go",
	"ttsd/main.go",
	"webd/main.go",
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
	for _, d := range daemons {
		b, err := os.ReadFile(filepath.Join(root, d))
		if err != nil {
			t.Errorf("%s: %v", d, err)
			continue
		}
		lines := strings.Split(string(b), "\n")
		notify := firstLine(lines, "signal.Notify(", "signal.NotifyContext(")
		listen := firstLine(lines, "ListenAndServe(", "ListenAndServeTLS(", "net.Listen(")
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
