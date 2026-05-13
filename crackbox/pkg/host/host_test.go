package host

import (
	"strings"
	"testing"

	"github.com/kronael/arizuko/crackbox/pkg/host/internal"
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
