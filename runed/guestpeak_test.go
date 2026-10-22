package runed

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTurnLogNamesThePeakGuestMemory pins plan-memory step 4: the log carries one
// number per turn, which RUNED_GUEST_MEM is then set against. An unread peak says
// so, because "0 MiB" would read as a guest that touched nothing.
func TestTurnLogNamesThePeakGuestMemory(t *testing.T) {
	for _, c := range []struct {
		peakKiB int64
		want    string
	}{
		{573_440, "Peak guest memory: 560 MiB\n"},
		{0, "Peak guest memory: unknown\n"},
	} {
		path := filepath.Join(t.TempDir(), "container.log")
		writeLog(path, Turn{Folder: "krons/content", Handle: "h1"},
			time.Minute, 0, false, true, c.peakKiB, "")
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), c.want) {
			t.Errorf("peak %d KiB: want %q in:\n%s", c.peakKiB, c.want, b)
		}
	}
}

// TestGuestPeakReadsMaxrssOfAProcessThatIsStillRunning covers the order a real
// turn has: the exit frame arrives while QEMU still runs, so `after` must stop it
// and read the rusage the kernel hands back at Wait.
func TestGuestPeakReadsMaxrssOfAProcessThatIsStillRunning(t *testing.T) {
	cmd := exec.Command("sleep", "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	p := &guestPeak{done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		p.kib = maxrssKiB(cmd.ProcessState)
		close(p.done)
	}()
	if got := p.after(cmd.Process, 10*time.Second); got <= 0 {
		t.Fatalf("peak = %d KiB, want the process's non-zero Maxrss", got)
	}
}

// A process that does not exit inside the grace has no rusage yet: the answer is
// 0 (unknown), never a half-written number.
func TestGuestPeakIsUnknownWhenTheProcessOutlivesGrace(t *testing.T) {
	cmd := exec.Command("sh", "-c", "trap '' TERM; sleep 60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	p := &guestPeak{done: make(chan struct{})}
	if got := p.after(cmd.Process, 50*time.Millisecond); got != 0 {
		t.Fatalf("peak = %d, want 0 for a process that has not been waited", got)
	}
}
