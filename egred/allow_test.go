package main

import (
	"testing"
)

// matchHost test table is ported from crackbox/internal/vm/proxy_test.go
// (TestProxyIsAllowed). The original tests asserted ProxyServer.isAllowed
// behavior; matchHost preserves the same semantics minus AllowAll.

func TestMatchHost(t *testing.T) {
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
	}
	for _, tc := range tests {
		got := matchHost(tc.allowlist, tc.host)
		if got != tc.want {
			t.Errorf("%s: matchHost(%v, %q) = %v, want %v",
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
		got := looksLikeDomain(tc.in)
		if got != tc.want {
			t.Errorf("looksLikeDomain(%q) = %v, want %v", tc.in, got, tc.want)
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
		got := looksLikeIP(tc.in)
		if got != tc.want {
			t.Errorf("looksLikeIP(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestAllowlistSetLookupAllow(t *testing.T) {
	a := NewAllowlist()
	a.Set("10.99.0.5", "acme/eng", []string{"github.com", "anthropic.com"})

	folder, list, ok := a.Lookup("10.99.0.5")
	if !ok || folder != "acme/eng" || len(list) != 2 {
		t.Fatalf("Lookup mismatch: %v %v %v", folder, list, ok)
	}

	if folder, ok := a.Allow("10.99.0.5", "api.github.com"); !ok || folder != "acme/eng" {
		t.Errorf("Allow allowed-host: ok=%v folder=%q", ok, folder)
	}
	if _, ok := a.Allow("10.99.0.5", "evil.com"); ok {
		t.Errorf("Allow denied-host should fail")
	}
	if _, ok := a.Allow("10.99.0.99", "github.com"); ok {
		t.Errorf("Allow unknown-IP should fail")
	}

	a.Remove("10.99.0.5")
	if _, _, ok := a.Lookup("10.99.0.5"); ok {
		t.Errorf("Remove did not clear")
	}
}
