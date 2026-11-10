package buildgate

import (
	"errors"
	"os/exec"
	"testing"
)

// TestPackageScriptsAreNotIgnored pins BUGS L662: a `*.py` pattern kept every
// package script out of git, so the release shipped skills whose scripts did
// not exist. The paths need not exist; --no-index asks the patterns alone.
func TestPackageScriptsAreNotIgnored(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, path := range []string{
		"packages/p/skills/s/scripts/x.py",
		"ant/skills/s/scripts/x.py",
		"ant/hooks/x.py",
		"packages/p/skills/s/scripts/x.sh",
	} {
		cmd := exec.Command("git", "check-ignore", "--no-index", path)
		cmd.Dir = repoRoot
		_, err := cmd.Output()
		var exit *exec.ExitError
		switch {
		case err == nil:
			t.Errorf("%s is ignored; run git check-ignore -v --no-index %s", path, path)
		case errors.As(err, &exit) && exit.ExitCode() == 1:
		default:
			t.Fatalf("git check-ignore %s: %v", path, err)
		}
	}
}
