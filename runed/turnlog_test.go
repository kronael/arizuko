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
		time.Minute, 0 /*code*/, false /*timedOut*/, false /*framed*/, 0, agentSaid)

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
		time.Minute, 0, false, true /*framed*/, 0, "chatty but harmless")

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

// TestLineBufferSaysWhatItDropped pins BUGS `L385`. The guest's log is capped at
// 10 MB, and past the cap `add` wrote nothing and said nothing — so the turn whose
// log is worth reading, the one that produced 10 MB of stderr, ended mid-line and
// read as a guest that stopped talking.
//
// This is the same silence `L375` cost a day to: an absent tail must name itself.
func TestLineBufferSaysWhatItDropped(t *testing.T) {
	var l lineBuffer

	l.add("the first line")
	if got := l.String(); got != "the first line\n" {
		t.Errorf("under the cap the buffer is the lines alone, got %q", got)
	}

	// One line over the cap, then three more.
	l.add(strings.Repeat("x", maxOutputSize))
	for range 3 {
		l.add("dropped")
	}
	got := l.String()
	if strings.Contains(got, "dropped\n") {
		t.Error("past the cap the buffer must stop writing")
	}
	for _, want := range []string{"3 more line(s) dropped", "10 MB cap"} {
		if !strings.Contains(got, want) {
			t.Errorf("the buffer must name what it dropped, %q absent from the tail:\n%s",
				want, got[len(got)-200:])
		}
	}
}
