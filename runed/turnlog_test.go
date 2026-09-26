package runed

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTurnLogWritesTheStderrWhenTheGuestSentNoExitFrame pins BUGS L375. A turn
// whose guest never sent an exit frame carries QEMU's own status, which was 0
// while the agent had exited 1 — so the log recorded a clean turn and dropped the
// stderr, for the one failure that needed reading.
//
// The absent frame is the error condition, not the number beside it.
func TestTurnLogWritesTheStderrWhenTheGuestSentNoExitFrame(t *testing.T) {
	const agentSaid = "guest-init: agent wait: exit status 1"
	path := filepath.Join(t.TempDir(), "container.log")

	writeLog(path, Turn{Folder: "krons/content", Handle: "h1"},
		time.Minute, 0 /*code*/, false /*timedOut*/, false /*framed*/, agentSaid)

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, want := range []string{"=== Stderr ===", agentSaid, "the guest sent no exit frame"} {
		if !strings.Contains(got, want) {
			t.Errorf("the log must carry %q:\n%s", want, got)
		}
	}
}

// The dual: a turn that DID report, and reported success, keeps its short form.
// Writing every agent's stderr to disk on every green turn is what the summary
// form exists to avoid.
func TestTurnLogStaysShortWhenTheGuestReportedSuccess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "container.log")

	writeLog(path, Turn{Folder: "krons/content", Handle: "h1"},
		time.Minute, 0, false, true /*framed*/, "chatty but harmless")

	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	if strings.Contains(got, "=== Stderr ===") || strings.Contains(got, "chatty but harmless") {
		t.Errorf("a reported, successful turn keeps the summary form:\n%s", got)
	}
	if strings.Contains(got, "the guest sent no exit frame") {
		t.Errorf("the exit code IS the agent's here, so it must not be qualified:\n%s", got)
	}
	if !strings.Contains(got, "=== Input Summary ===") {
		t.Errorf("the summary form is what it should have written:\n%s", got)
	}
}
