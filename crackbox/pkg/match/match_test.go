package match

import "testing"

// Host test table is ported from crackbox/internal/vm/proxy_test.go
// (TestProxyIsAllowed). The original tests asserted ProxyServer.isAllowed
// behavior; Host preserves the same semantics minus AllowAll.

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
		{"ip in allowlist skipped (no host match)", []string{"1.2.3.4"}, "1.2.3.4", false},
		{"multiple entries", []string{"foo.com", "bar.org", "baz.net"}, "api.bar.org", true},
		{"wildcard allows anything", []string{"*"}, "evil.example", true},
		{"wildcard alongside others", []string{"github.com", "*"}, "anywhere.org", true},
		{"wildcard ignored if not literal star", []string{"*.com"}, "github.com", false},
	}
	for _, tc := range tests {
		got := Host(tc.allowlist, tc.host)
		if got != tc.want {
			t.Errorf("%s: Host(%v, %q) = %v, want %v",
				tc.name, tc.allowlist, tc.host, got, tc.want)
		}
	}
}

func TestLooksLikeDomain(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"github.com", true},
		{"api.github.com", true},
		{"a.b.c.d", true},
		{"localhost", false},
		{"1.2.3.4", false},
		{"github.com/path", false},
		{"-bad.com", false},
		{"good-.com", false},
		{"", false},
	}
	for _, tc := range tests {
		got := LooksLikeDomain(tc.in)
		if got != tc.want {
			t.Errorf("LooksLikeDomain(%q) = %v, want %v", tc.in, got, tc.want)
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
