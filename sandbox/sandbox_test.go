package sandbox

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/sandbox/internal"
)

func TestValidateInstanceID(t *testing.T) {
	tests := []struct {
		id    string
		valid bool
	}{
		{"default", true},
		{"my-instance", true},
		{"inst_01", true},
		{"a", true},
		// exactly 32 chars
		{"12345678901234567890123456789012", true},
		// invalid
		{"", false},
		{"has space", false},
		{"has@special", false},
		// 33 chars
		{"123456789012345678901234567890123", false},
		{"inst/path", false},
		{"inst.dot", false},
		{"inst:colon", false},
	}
	for _, tt := range tests {
		err := validateInstanceID(tt.id)
		if (err == nil) != tt.valid {
			t.Errorf("validateInstanceID(%q): got err=%v, want valid=%v", tt.id, err, tt.valid)
		}
	}
}

// TestBridgeTapNames verifies all ifnames stay ≤15 chars (IFNAMSIZ-1)
// across a range of InstanceID values including the 32-char maximum.
func TestBridgeTapNames(t *testing.T) {
	instanceIDs := []string{
		"a",
		"default",
		"my-instance-01",
		"inst_2025_prod",
		strings.Repeat("x", 32), // 32-char max
		"abcdefghijklmnopqrstuvwxyz123456",
		"ALLCAPS-INSTANCE",
		"mix_ed-01",
	}

	for _, id := range instanceIDs {
		bridge := internal.BridgeName(id)
		if len(bridge) > 15 {
			t.Errorf("BridgeName(%q) = %q (%d chars) > 15", id, bridge, len(bridge))
		}

		for idx := 0; idx <= 255; idx += 64 {
			tap := internal.TapName(id, idx)
			if len(tap) > 15 {
				t.Errorf("TapName(%q, %d) = %q (%d chars) > 15", id, idx, tap, len(tap))
			}
		}
	}
}

// TestIPRangeFor verifies IPRangeFor is deterministic across repeated calls.
func TestIPRangeFor(t *testing.T) {
	ids := []string{"default", "my-instance", strings.Repeat("z", 32)}

	for _, id := range ids {
		r1 := internal.IPRangeFor(id)
		r2 := internal.IPRangeFor(id)
		if r1 != r2 {
			t.Errorf("IPRangeFor(%q) not deterministic: %q vs %q", id, r1, r2)
		}
		if !strings.HasPrefix(r1, "10.") || !strings.HasSuffix(r1, ".0/24") {
			t.Errorf("IPRangeFor(%q) = %q: unexpected format", id, r1)
		}
	}
}

// TestIPRangeForUniqueness verifies distinct instanceIDs produce distinct ranges
// (no guarantee, but the SHA hash makes collisions negligible for our test cases).
func TestIPRangeForUniqueness(t *testing.T) {
	ids := []string{"a", "b", "default", "prod", "staging"}
	seen := make(map[string]string)
	for _, id := range ids {
		r := internal.IPRangeFor(id)
		if prev, ok := seen[r]; ok {
			t.Errorf("IPRangeFor collision: %q and %q both produce %q", prev, id, r)
		}
		seen[r] = id
	}
}

// The public Host API above Spawn runs without a hypervisor: New's validation,
// List reading the on-disk records, and the not-found paths of Stop and Exec.
// Those were 0% — the package sat at 3.1% because only the pure helpers were
// reached.

func TestNewRejectsABadInstanceIDOrAMissingDataDir(t *testing.T) {
	if _, err := New("", t.TempDir()); err == nil {
		t.Error("New accepted an empty instance id")
	}
	if _, err := New("has/slash", t.TempDir()); err == nil {
		t.Error("New accepted an instance id with a path separator")
	}
	if _, err := New("ok", ""); err == nil {
		t.Error("New accepted an empty dataDir; VM state would land somewhere unpredictable")
	}
	h, err := New("krons_1-a", t.TempDir())
	if err != nil {
		t.Fatalf("New rejected a valid pair: %v", err)
	}
	if h == nil {
		t.Fatal("New returned a nil Host with no error")
	}
}

// A fresh host has no VMs. The empty case has to be an empty slice and no
// error — a caller reaping stale VMs treats an error as "cannot tell" and
// leaves everything alone.
func TestListIsEmptyOnAFreshHost(t *testing.T) {
	h, err := New("fresh", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	got, err := h.List()
	if err != nil {
		t.Fatalf("List on a fresh host: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List = %+v, want empty", got)
	}
}

// Stop and Exec against a handle that names no VM must fail before they touch
// qemu or ssh. Two hosts sharing a data dir but not an instance id must not
// see each other's VMs either — the instance id is the namespace.
func TestStopAndExecRefuseAnUnknownHandle(t *testing.T) {
	dir := t.TempDir()
	h, err := New("one", dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Stop(Handle{ID: "no-such-vm", IP: "10.1.1.9"}); err == nil {
		t.Error("Stop accepted a handle naming no VM")
	}
	code, stdout, stderr, err := h.Exec(Handle{ID: "no-such-vm", IP: "10.1.1.9"}, []string{"true"}, nil)
	if err == nil {
		t.Error("Exec accepted a handle naming no VM; it would ssh to an arbitrary address")
	}
	if code != -1 || stdout != nil || stderr != nil {
		t.Errorf("Exec on an unknown handle returned (%d, %q, %q), want (-1, nil, nil)", code, stdout, stderr)
	}
}

func TestInstanceIDNamespacesTwoHostsOnOneDataDir(t *testing.T) {
	dir := t.TempDir()
	a, err := New("alpha", dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New("beta", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range []*Host{a, b} {
		got, err := h.List()
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("List = %+v, want empty", got)
		}
	}
	// The namespaces are separate directories, so one host's teardown cannot
	// name the other's VM.
	if err := a.Stop(Handle{ID: "beta-vm"}); err == nil {
		t.Error("alpha stopped a VM id it does not own")
	}
}
