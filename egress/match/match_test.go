package match

import (
	"strings"
	"testing"
)

// Host test table: AllowAll is an explicit mode on Allowed, not a "*" list
// entry (spec 6/25 1b).

func TestHost(t *testing.T) {
	tests := []struct {
		name      string
		allowlist []string
		host      string
		want      bool
	}{
		{"exact domain match", []string{"github.com"}, "github.com", true},
		{"subdomain match", []string{"github.com"}, "api.github.com", true},
		{"deep subdomain match", []string{"github.com"}, "raw.githubusercontent.github.com", true},
		{"case insensitive allowlist", []string{"GitHub.COM"}, "github.com", true},
		{"case insensitive host", []string{"github.com"}, "GITHUB.com", true},
		{"trailing dot stripped from host", []string{"github.com"}, "github.com.", true},
		{"not in allowlist", []string{"github.com"}, "evil.com", false},
		{"partial suffix not allowed", []string{"hub.com"}, "github.com", false},
		{"empty allowlist blocks all", nil, "github.com", false},
		{"multiple entries", []string{"foo.com", "bar.org", "baz.net"}, "api.bar.org", true},
		{"leading glob matches subdomain", []string{"*.github.com"}, "api.github.com", true},
		{"leading glob matches apex too", []string{"*.github.com"}, "github.com", true},
		// A single label matches exactly and never as a suffix: `localhost` and a
		// container service name are real dotless hosts, but so is `com`, and one
		// such row would otherwise allow the whole TLD (BUGS L209).
		{"single label does not grant its TLD", []string{"com"}, "github.com", false},
		{"single label still matches itself", []string{"localhost"}, "localhost", true},
		{"single label is not a parent", []string{"localhost"}, "evil.localhost", false},
	}
	for _, tc := range tests {
		got := Host(tc.allowlist, tc.host)
		if got != tc.want {
			t.Errorf("%s: Host(%v, %q) = %v, want %v",
				tc.name, tc.allowlist, tc.host, got, tc.want)
		}
	}
}

// TestAllowed pins deny-beats-allow: a deny entry carves an exception out of
// a broad allow AND out of allow-all, and covers its own subtree.
func TestAllowed(t *testing.T) {
	tests := []struct {
		name     string
		allow    []string
		deny     []string
		allowAll bool
		host     string
		want     bool
	}{
		{"allow only", []string{"example.com"}, nil, false, "example.com", true},
		{"deny carves out of broad allow", []string{"example.com"}, []string{"internal.example.com"}, false, "internal.example.com", false},
		{"subdomain under a denied name stays denied", []string{"example.com"}, []string{"internal.example.com"}, false, "db.internal.example.com", false},
		{"sibling of the carve-out still allowed", []string{"example.com"}, []string{"internal.example.com"}, false, "api.example.com", true},
		{"apex still allowed beside the carve-out", []string{"example.com"}, []string{"internal.example.com"}, false, "example.com", true},
		{"deny beats allow-all", nil, []string{"github.com"}, true, "github.com", false},
		{"allow-all passes what deny does not name", nil, []string{"github.com"}, true, "anywhere.org", true},
		{"nothing allowed by default", nil, nil, false, "github.com", false},
	}
	for _, tc := range tests {
		got := Allowed(tc.allow, tc.deny, tc.allowAll, tc.host)
		if got != tc.want {
			t.Errorf("%s: Allowed(%v, %v, %v, %q) = %v, want %v",
				tc.name, tc.allow, tc.deny, tc.allowAll, tc.host, got, tc.want)
		}
	}
}

// TestValidEntry pins one rejection per defect class — every one of these is
// a rule an operator could write that would silently never match.
func TestValidEntry(t *testing.T) {
	valid := []string{
		"example.com",
		"api.example.com",
		"localhost",
		"com",
		"*.example.com",
		"Example.COM.",
		"http-intake.logs.us5.datadoghq.com",
	}
	for _, e := range valid {
		if err := ValidEntry(e); err != nil {
			t.Errorf("ValidEntry(%q) = %v, want nil", e, err)
		}
	}

	invalid := []struct {
		entry string
		frag  string // the error must name the defect
	}{
		{"", "empty"},
		{"*", "allow-all"},
		{"https://example.com", "scheme"},
		{"user@example.com", "credentials"},
		{"example.com/path", "path"},
		{"example.com:443", "port"},
		{"1.2.3.4", "IP"},
		{"10.0.0.0/8", "IP"},
		{"::1", "IP"},
		{"api.*.example.com", "wildcard"},
		{"example.*", "wildcard"},
		{"*.", "not a host name"},
		{"-bad.com", "not a host name"},
	}
	for _, tc := range invalid {
		err := ValidEntry(tc.entry)
		if err == nil {
			t.Errorf("ValidEntry(%q) = nil, want error naming %q", tc.entry, tc.frag)
			continue
		}
		if !strings.Contains(err.Error(), tc.frag) {
			t.Errorf("ValidEntry(%q) = %q, want it to name %q", tc.entry, err, tc.frag)
		}
	}
}

func TestValidateLists(t *testing.T) {
	if err := ValidateLists([]string{"example.com", "github.com"}, []string{"internal.example.com"}); err != nil {
		t.Errorf("valid lists rejected: %v", err)
	}
	if err := ValidateLists([]string{"https://example.com"}, nil); err == nil {
		t.Error("invalid allow entry accepted")
	}
	if err := ValidateLists(nil, []string{"example.com:443"}); err == nil {
		t.Error("invalid deny entry accepted")
	}
	if err := ValidateLists([]string{"example.com", "Example.com."}, nil); err == nil {
		t.Error("duplicate (normalized) allow entry accepted")
	}
	if err := ValidateLists([]string{"example.com"}, []string{"*.example.com"}); err == nil {
		t.Error("host in both lists accepted")
	}
	// The carve-out shape is NOT a both-lists violation: the entries differ.
	if err := ValidateLists([]string{"example.com"}, []string{"internal.example.com"}); err != nil {
		t.Errorf("deny carve-out rejected: %v", err)
	}
}

func TestNormalizeEntry(t *testing.T) {
	tests := []struct{ in, want string }{
		{"Example.COM.", "example.com"},
		{"*.example.com", "example.com"},
		{"example.com", "example.com"},
	}
	for _, tc := range tests {
		if got := NormalizeEntry(tc.in); got != tc.want {
			t.Errorf("NormalizeEntry(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestLooksLikeIP(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"1.2.3.4", true},
		{"::1", true},
		{"10.0.0.0/8", true},
		{"github.com", false},
		{"not-an-ip", false},
	}
	for _, tc := range tests {
		got := LooksLikeIP(tc.in)
		if got != tc.want {
			t.Errorf("LooksLikeIP(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
