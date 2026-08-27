package internal

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Everything below Start needs a hypervisor. Everything ABOVE it — finding the
// helper binaries, reading the on-disk VM record, and the not-found and
// soft-delete branches of the lifecycle — runs on any host and was untested.

// ---------------------------------------------------------------------------
// LibexecDir
// ---------------------------------------------------------------------------

func fakeLibexec(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The operator override is checked first and, when it is wrong, must FAIL
// rather than fall through to a system path. Falling through would run the
// packaged helpers while the operator believes their own are in use.
func TestLibexecDirHonoursTheOverrideAndFailsLoudWhenItIsWrong(t *testing.T) {
	good := fakeLibexec(t, "crackbox-cloudinit", "crackbox-tap")
	t.Setenv("CRACKBOX_LIBEXEC", good)
	got, err := LibexecDir()
	if err != nil {
		t.Fatalf("a complete override directory was rejected: %v", err)
	}
	if got != good {
		t.Errorf("LibexecDir = %q, want the override %q", got, good)
	}

	// Half a directory is not a directory: crackbox-tap alone cannot build the
	// network, and silently using it would fail deep inside Start.
	half := fakeLibexec(t, "crackbox-tap")
	t.Setenv("CRACKBOX_LIBEXEC", half)
	if _, err := LibexecDir(); err == nil {
		t.Fatal("an override missing crackbox-cloudinit was accepted")
	}

	t.Setenv("CRACKBOX_LIBEXEC", filepath.Join(t.TempDir(), "does-not-exist"))
	if _, err := LibexecDir(); err == nil {
		t.Fatal("an override pointing nowhere was accepted")
	}
}

func TestHasToolsNeedsBothHelpers(t *testing.T) {
	cases := map[string][]string{
		"both":        {"crackbox-cloudinit", "crackbox-tap"},
		"cloudinit":   {"crackbox-cloudinit"},
		"tap":         {"crackbox-tap"},
		"neither":     {},
		"wrong names": {"cloudinit", "tap"},
	}
	for name, files := range cases {
		dir := fakeLibexec(t, files...)
		want := name == "both"
		if got := hasTools(dir); got != want {
			t.Errorf("%s: hasTools = %v, want %v", name, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Get / List / Stop / Destroy without a hypervisor
// ---------------------------------------------------------------------------

func TestGetReturnsNilForAnUnknownVM(t *testing.T) {
	m := setupTestManager(t)
	meta, err := m.Get("no-such-vm")
	if err != nil {
		t.Fatalf("Get on an unknown id errored: %v", err)
	}
	if meta != nil {
		t.Errorf("Get returned %+v for an unknown id, want nil", meta)
	}
}

// Stop and Destroy must both refuse an id that is not there rather than tear
// down whatever the empty metadata happens to point at.
func TestStopAndDestroyRefuseAnUnknownVM(t *testing.T) {
	m := setupTestManager(t)
	if err := m.Stop("no-such-vm"); err == nil {
		t.Error("Stop accepted an unknown id")
	}
	if err := m.Destroy("no-such-vm"); err == nil {
		t.Error("Destroy accepted an unknown id")
	}
}

// A stopped VM has no qemu process, so Destroy takes the soft-delete path: it
// marks DeletedAt and leaves the record on disk for later cleanup, and the VM
// disappears from List without the directory being removed under a running
// operator's feet.
func TestDestroyOnAStoppedVMSoftDeletesIt(t *testing.T) {
	m := setupTestManager(t)
	meta := &Meta{ID: "vm-stopped", Name: "quiet", NetIndex: 1, CreatedAt: time.Now().UnixMilli()}
	if err := m.saveMeta(meta); err != nil {
		t.Fatal(err)
	}
	if got := m.detectState(meta); got != VMStateStopped {
		t.Fatalf("a VM with no qemu.pid detected as %v, want stopped", got)
	}

	if err := m.Destroy("vm-stopped"); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	back, err := m.loadMeta("vm-stopped")
	if err != nil {
		t.Fatal(err)
	}
	if back == nil {
		t.Fatal("Destroy removed the record instead of marking it deleted")
	}
	if back.DeletedAt == 0 {
		t.Error("DeletedAt was not set")
	}
	if got := m.detectState(back); got != VMStateDeleted {
		t.Errorf("state after destroy = %v, want deleted", got)
	}
}

// List reports RUNNING VMs only. A stopped one, and a soft-deleted one, are
// both absent — a caller iterating List to reap or exec must not touch either.
func TestListReportsOnlyRunningVMs(t *testing.T) {
	m := setupTestManager(t)
	now := time.Now().UnixMilli()
	for _, meta := range []*Meta{
		{ID: "vm-stopped", Name: "a", NetIndex: 1, IP: "10.1.1.2", CreatedAt: now},
		{ID: "vm-deleted", Name: "b", NetIndex: 2, IP: "10.1.1.3", CreatedAt: now, DeletedAt: now},
	} {
		if err := m.saveMeta(meta); err != nil {
			t.Fatal(err)
		}
	}

	got, err := m.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List returned %+v; neither a stopped nor a deleted VM is running", got)
	}

	// A live pid makes one of them running. os.Getpid() is this process, which
	// is certainly alive — the same signal readPID+isRunning look for.
	running := &Meta{ID: "vm-running", Name: "c", NetIndex: 3, IP: "10.1.1.4", CreatedAt: now}
	if err := m.saveMeta(running); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.vmDir("vm-running"), "qemu.pid"),
		[]byte(itoa(os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.detectState(running); got != VMStateRunning {
		t.Fatalf("a VM whose pid is alive detected as %v, want running", got)
	}

	got, err = m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "vm-running" {
		t.Errorf("List = %+v, want just vm-running", got)
	}
	if got[0].IP != "10.1.1.4" {
		t.Errorf("the entry carries IP %q, want the record's", got[0].IP)
	}
}

// A garbage pid file must read as 0 rather than as some other process's id —
// detectState would otherwise call a dead VM running because pid 1 exists.
func TestReadPIDIsZeroForAMissingOrGarbageFile(t *testing.T) {
	m := setupTestManager(t)
	dir := t.TempDir()

	if got := m.readPID(dir); got != 0 {
		t.Errorf("readPID with no file = %d, want 0", got)
	}
	for _, body := range []string{"", "not-a-pid", "\n"} {
		if err := os.WriteFile(filepath.Join(dir, "qemu.pid"), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := m.readPID(dir); got != 0 {
			t.Errorf("readPID(%q) = %d, want 0", body, got)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "qemu.pid"), []byte("4242\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := m.readPID(dir); got != 4242 {
		t.Errorf("readPID = %d, want 4242", got)
	}
}

// CleanupNetworkRules shells out to iptables for every rule it removes. A VM
// that never got an IP has no rules, and must not run a single command —
// otherwise destroying a VM that failed to start floods the host with
// iptables calls that all fail.
func TestCleanupNetworkRulesIsANoOpWithoutAnIP(t *testing.T) {
	m := setupTestManager(t)
	m.CleanupNetworkRules(&Meta{ID: "x", AllowList: []string{"1.2.3.4", "example.com"}})
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
