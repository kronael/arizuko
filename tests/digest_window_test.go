package tests

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const digestWindow = "../packages/company-brain/skills/digest/scripts/window.py"

// runWindow runs window.py against a temp HOME and state file. state "" means
// no state file.
func runWindow(t *testing.T, state string, args ...string) (string, error) {
	t.Helper()
	home := t.TempDir()
	sp := filepath.Join(home, "state.json")
	if state != "" {
		if err := os.WriteFile(sp, []byte(state), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command("python3", append([]string{digestWindow, "--state", sp, "--out", filepath.Join(home, "run")}, args...)...)
	cmd.Env = append(os.Environ(), "HOME="+home, "DIGEST_ROOT="+filepath.Join(home, "root"))
	if err := os.MkdirAll(filepath.Join(home, "root"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const postedState = `{"last_end":"2026-11-07T09:00:20Z","posted_run":"20261107"}`

func TestDigestWindowRefusesEndOfRunThatNeverPosted(t *testing.T) {
	out, err := runWindow(t, postedState, "--end", "2026-11-08T09:00:20Z")
	if err == nil || !strings.Contains(out, "run without --end") {
		t.Fatalf("want refusal naming the dead-run path, got err=%v out=%s", err, out)
	}
	if out, err = runWindow(t, "", "--end", "2026-11-08T09:00:20Z"); err == nil {
		t.Fatalf("no state: want refusal, got %s", out)
	}
}

func TestDigestWindowRedoAtPostedEnd(t *testing.T) {
	out, err := runWindow(t, postedState, "--end", "2026-11-07T09:00:20Z")
	if err != nil || !strings.Contains(out, "END=2026-11-07T09:00:20Z") {
		t.Fatalf("redo at posted END must work: err=%v out=%s", err, out)
	}
}

func TestDigestWindowDeadRunNextRunIsMissed(t *testing.T) {
	state := `{"last_end":"2026-01-01T00:00:00Z","posted_run":"20260101"}`
	out, err := runWindow(t, state)
	if err != nil || !strings.Contains(out, "MISSED=1") || !strings.Contains(out, "START=2026-01-01T00:00:00Z") {
		t.Fatalf("want MISSED=1 from last_end: err=%v out=%s", err, out)
	}
}
