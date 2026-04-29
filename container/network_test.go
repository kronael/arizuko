package container

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func TestFolderNetwork(t *testing.T) {
	cases := []struct {
		prefix, folder, want string
	}{
		{"arizuko_krons", "atlas", "arizuko_krons_atlas"},
		{"arizuko_krons", "atlas/support", "arizuko_krons_atlas-support"},
		{"arizuko_solo", "inbox", "arizuko_solo_inbox"},
		{"arizuko", "x/y/z", "arizuko_x-y-z"},
	}
	for _, c := range cases {
		if got := FolderNetwork(c.prefix, c.folder); got != c.want {
			t.Errorf("FolderNetwork(%q, %q) = %q, want %q",
				c.prefix, c.folder, got, c.want)
		}
	}
}

// TestPickFolderSubnetDeterministic — same folder always hashes to the same /24.
func TestPickFolderSubnetDeterministic(t *testing.T) {
	mgr := &netMgr{perFolder: map[string]*sync.Mutex{}, allocated: map[string]bool{}}
	a, err := pickFolderSubnet(mgr, "10.99.0.0/16", "atlas/support")
	if err != nil {
		t.Fatal(err)
	}
	b, err := pickFolderSubnet(mgr, "10.99.0.0/16", "atlas/support")
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Errorf("non-deterministic: %s vs %s", a, b)
	}
	// Subnet must be inside parent.
	_, parent, _ := net.ParseCIDR("10.99.0.0/16")
	ip, _, _ := net.ParseCIDR(a)
	if !parent.Contains(ip) {
		t.Errorf("%s not inside parent 10.99.0.0/16", a)
	}
	if !strings.HasSuffix(a, "/24") {
		t.Errorf("expected /24, got %s", a)
	}
}

// TestPickFolderSubnetCollision — once a /24 is allocated, the next probe
// for a folder that hashes there picks the next free /24.
func TestPickFolderSubnetCollision(t *testing.T) {
	mgr := &netMgr{perFolder: map[string]*sync.Mutex{}, allocated: map[string]bool{}}
	first, err := pickFolderSubnet(mgr, "10.99.0.0/16", "folder-a")
	if err != nil {
		t.Fatal(err)
	}
	mgr.markAllocated(first)
	second, err := pickFolderSubnet(mgr, "10.99.0.0/16", "folder-a")
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Errorf("collision not avoided: both got %s", first)
	}
	mgr.markAllocated(second)
	third, err := pickFolderSubnet(mgr, "10.99.0.0/16", "folder-a")
	if err != nil {
		t.Fatal(err)
	}
	if third == first || third == second {
		t.Errorf("third probe collided: %s in {%s,%s}", third, first, second)
	}
}

// TestPickFolderSubnetExhausted — when every /24 is taken, allocation
// fails cleanly instead of looping forever.
func TestPickFolderSubnetExhausted(t *testing.T) {
	mgr := &netMgr{perFolder: map[string]*sync.Mutex{}, allocated: map[string]bool{}}
	// /22 -> 4 /24 slots: 10.99.0.0, .1.0, .2.0, .3.0.
	for i := 0; i < 4; i++ {
		mgr.markAllocated(fmt.Sprintf("10.99.%d.0/24", i))
	}
	if _, err := pickFolderSubnet(mgr, "10.99.0.0/22", "x"); err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
}

// TestPickFolderSubnetSlotCounts — verify slot math for various parent prefixes.
func TestPickFolderSubnetSlotCounts(t *testing.T) {
	cases := []struct {
		parent string
		slots  int
	}{
		{"10.99.0.0/16", 256},
		{"10.99.0.0/20", 16},
		{"10.99.0.0/22", 4},
		{"10.99.0.0/24", 1},
	}
	for _, c := range cases {
		mgr := &netMgr{perFolder: map[string]*sync.Mutex{}, allocated: map[string]bool{}}
		seen := map[string]bool{}
		for i := 0; i < c.slots; i++ {
			s, err := pickFolderSubnet(mgr, c.parent, fmt.Sprintf("f-%d", i))
			if err != nil {
				t.Fatalf("%s slot %d: %v", c.parent, i, err)
			}
			if seen[s] {
				t.Fatalf("%s: duplicate slot %s", c.parent, s)
			}
			seen[s] = true
			mgr.markAllocated(s)
		}
		if _, err := pickFolderSubnet(mgr, c.parent, "overflow"); err == nil {
			t.Errorf("%s: expected exhaustion at %d", c.parent, c.slots)
		}
	}
}

// TestPickFolderSubnetRejectsBadPrefix — /7 too wide, /25 too small.
func TestPickFolderSubnetRejectsBadPrefix(t *testing.T) {
	mgr := &netMgr{perFolder: map[string]*sync.Mutex{}, allocated: map[string]bool{}}
	if _, err := pickFolderSubnet(mgr, "10.0.0.0/7", "x"); err == nil {
		t.Error("expected /7 to be rejected")
	}
	if _, err := pickFolderSubnet(mgr, "10.99.0.0/25", "x"); err == nil {
		t.Error("expected /25 to be rejected")
	}
}

// TestNetMgrFolderLockPerFolder — different folders get distinct mutexes,
// same folder gets the same mutex. Race-detector validates concurrent use.
func TestNetMgrFolderLockPerFolder(t *testing.T) {
	mgr := &netMgr{perFolder: map[string]*sync.Mutex{}, allocated: map[string]bool{}}
	a1 := mgr.folderLock("a")
	a2 := mgr.folderLock("a")
	b := mgr.folderLock("b")
	if a1 != a2 {
		t.Error("same folder must return same mutex")
	}
	if a1 == b {
		t.Error("different folders must return different mutexes")
	}
}

// TestEnsureFolderNetworkConcurrency — N concurrent ensures for the same
// folder run docker network create at most once. We stub execCommand with
// a tiny shell that records call types via output to a tempfile.
func TestEnsureFolderNetworkConcurrency(t *testing.T) {
	prev := execCommand
	t.Cleanup(func() {
		execCommand = prev
		// Reset the package-global mgr so subsequent tests start clean.
		defaultNetMgr = &netMgr{
			perFolder: map[string]*sync.Mutex{},
			allocated: map[string]bool{},
		}
	})

	defaultNetMgr = &netMgr{
		perFolder: map[string]*sync.Mutex{},
		allocated: map[string]bool{},
	}

	var (
		creates  atomic.Int32
		connects atomic.Int32
		inspects atomic.Int32
	)
	// First inspect (creates==0) returns nothing & exits 1 (network missing).
	// After create succeeds, subsequent inspects return the subnet.
	execCommand = func(name string, args ...string) *exec.Cmd {
		if len(args) >= 2 && args[0] == "network" {
			switch args[1] {
			case "inspect":
				inspects.Add(1)
				if creates.Load() == 0 {
					return exec.Command("/bin/sh", "-c", "exit 1")
				}
				return exec.Command("/bin/sh", "-c", "echo 10.99.42.0/24")
			case "create":
				creates.Add(1)
				return exec.Command("/bin/true")
			case "connect":
				connects.Add(1)
				return exec.Command("/bin/true")
			}
		}
		return exec.Command("/bin/true")
	}

	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := ensureFolderNetwork(
				"arizuko_test", "arizuko_crackbox_test",
				"10.99.0.0/16", "atlas/support")
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("ensureFolderNetwork: %v", err)
		}
	}

	if c := creates.Load(); c > 1 {
		t.Errorf("network create called %d times, want 1", c)
	}
	if connects.Load() < 1 {
		t.Errorf("network connect not called")
	}
}

// TestEnsureFolderNetworkRequiresPrefix — empty prefix or container is rejected.
func TestEnsureFolderNetworkRequiresPrefix(t *testing.T) {
	if _, _, err := ensureFolderNetwork("", "cb", "10.99.0.0/16", "f"); err == nil {
		t.Error("expected error for empty prefix")
	}
	if _, _, err := ensureFolderNetwork("p", "", "10.99.0.0/16", "f"); err == nil {
		t.Error("expected error for empty crackbox container")
	}
}
