package audit

import (
	"os"
	"strings"
	"testing"
)

// A prune that no daemon starts deletes nothing while its own tests pass. The
// four daemons that own an audit_log start the loop in main; the three that
// record into routd's do not, so one log has one pruner.
func TestEachOwnerStartsThePruneLoop(t *testing.T) {
	for main, owns := range map[string]bool{
		"../routd/cmd/routd/main.go": true,
		"../authd/main.go":           true,
		"../onbod/main.go":           true,
		"../runed/cmd/runed/main.go": true,
		"../dashd/main.go":           false,
		"../webd/main.go":            false,
		"../proxyd/main.go":          false,
	} {
		src, err := os.ReadFile(main)
		if err != nil {
			t.Fatal(err)
		}
		starts := strings.Contains(string(src), "go audit.PruneLoop(")
		if starts != owns {
			t.Errorf("%s starts the prune loop = %v, want %v", main, starts, owns)
		}
	}
}
