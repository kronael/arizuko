package dyntest

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
)

// exitedPid answers the pid of a process that has run and been waited for, so
// the kernel holds nothing under it. A made-up number would prove less: this is
// the pid a test binary leaves behind when `go test -timeout` kills it.
func exitedPid(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start a process to end: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		t.Fatalf("wait for it: %v", err)
	}
	return pid
}

// TestOrphansTakesTheDeadPidOnly proves the reap's choice: a container whose
// creator is gone goes, and one whose creator still runs stays. The second half
// is what keeps `go test ./...` safe — every sibling test binary running beside
// this one owns a live container under its own live pid.
//
// The listing is the shape `docker ps -a --format '{{.Names}} {{.Label ...}}'`
// prints, measured against docker on 2026-09-19.
func TestOrphansTakesTheDeadPidOnly(t *testing.T) {
	dead, live := exitedPid(t), os.Getpid()
	listing := fmt.Sprintf("arizuko-dynstore-%d %d\narizuko-dynstore-%d %d\n",
		dead, dead, live, live)

	got := orphans(listing, alive)

	want := []string{fmt.Sprintf("arizuko-dynstore-%d", dead)}
	if !slices.Equal(got, want) {
		t.Fatalf("orphans(%q) = %v, want %v", listing, got, want)
	}
}

func TestOrphansKeepsARowItCannotRead(t *testing.T) {
	for _, listing := range []string{
		"",
		"\n \n",
		"arizuko-dynstore-7",
		"arizuko-dynstore-7 ",
		"arizuko-dynstore-7 seven",
		"arizuko-dynstore-7 0",
		"arizuko-dynstore-7 -7",
	} {
		if got := orphans(listing, alive); len(got) != 0 {
			t.Errorf("orphans(%q) = %v, want none", listing, got)
		}
	}
}

// TestReapRemovesADeadPidContainer runs the whole reap against docker: the
// listing flags, the format string and the removal. The unit test above cannot
// prove the format string, which is the half docker owns.
//
// Both containers are created and never started, so neither takes a port. They
// carry the fixture's label under names of their own, because the reap keys on
// the label and not on the name.
func TestReapRemovesADeadPidContainer(t *testing.T) {
	if testing.Short() {
		t.Skip("needs docker")
	}
	deadName := fmt.Sprintf("dyntest-reap-dead-%d", os.Getpid())
	liveName := fmt.Sprintf("dyntest-reap-live-%d", os.Getpid())
	create(t, deadName, exitedPid(t))
	create(t, liveName, os.Getpid())

	if err := reap(); err != nil {
		t.Fatalf("reap: %v", err)
	}

	if exists(t, deadName) {
		t.Errorf("%s survived the reap", deadName)
	}
	if !exists(t, liveName) {
		t.Errorf("%s was reaped while its process runs", liveName)
	}
}

func create(t *testing.T, name string, pid int) {
	t.Helper()
	out, err := docker("create", "--name", name,
		"--label", fmt.Sprintf("%s=%d", pidLabel, pid), Image, "true")
	if err != nil {
		t.Fatalf("create %s: %v: %s", name, err, out)
	}
	t.Cleanup(func() { _, _ = docker("rm", "-f", name) })
}

func exists(t *testing.T, name string) bool {
	t.Helper()
	out, err := docker("ps", "-a", "--filter", "name=^"+name+"$", "--format", "{{.Names}}")
	if err != nil {
		t.Fatalf("look for %s: %v: %s", name, err, out)
	}
	return out == name
}
