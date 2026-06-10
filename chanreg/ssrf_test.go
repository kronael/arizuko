package chanreg

import "testing"

// validateAdapterURL is the SSRF gate: adapter URLs must be http(s) and, unless
// CHANNEL_REGISTER_ALLOW_PUBLIC=1, resolve to private/loopback/link-local
// addresses. A regression here opens an SSRF hole — a rogue CHANNEL_SECRET
// holder could register a public/metadata URL and have routd POST to it.
func TestValidateAdapterURL_Reject(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "")
	for _, tt := range []struct {
		name, url string
	}{
		{"public_ipv4", "http://8.8.8.8:9001"},
		{"public_ipv4_no_port", "https://1.1.1.1"},
		{"public_ipv6", "http://[2606:4700:4700::1111]:9001"},
		{"scheme_file", "file:///etc/passwd"},
		{"scheme_gopher", "gopher://10.0.0.1:70"},
		{"scheme_ftp", "ftp://10.0.0.1"},
		{"missing_host", "http://"},
		{"unparseable", "http://[::1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateAdapterURL(tt.url); err == nil {
				t.Fatalf("expected reject for %s, got nil", tt.url)
			}
		})
	}
}

func TestValidateAdapterURL_Accept(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "")
	for _, tt := range []struct {
		name, url string
	}{
		{"loopback", "http://127.0.0.1:9001"},
		{"loopback_v6", "http://[::1]:9001"},
		{"private_10", "http://10.0.0.5:8080"},
		{"private_192", "http://192.168.1.10:8080"},
		{"private_172", "http://172.16.0.1:8080"},
		// Link-local (incl. 169.254.169.254 cloud metadata) is private per
		// isPrivateAddr: the SSRF concern is public egress, not link-local.
		{"link_local", "http://169.254.169.254/latest/meta-data"},
		// Docker service names resolve only inside the compose network; a
		// hostname that fails to resolve here is allowed (the send fails later,
		// not an SSRF vector).
		{"docker_service_name", "http://teled:8080"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateAdapterURL(tt.url); err != nil {
				t.Fatalf("expected accept for %s, got %v", tt.url, err)
			}
		})
	}
}

// CHANNEL_REGISTER_ALLOW_PUBLIC=1 is the dev escape hatch — a public IP that
// would otherwise be rejected must pass.
func TestValidateAdapterURL_AllowPublicOverride(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "1")
	if err := validateAdapterURL("http://8.8.8.8:9001"); err != nil {
		t.Fatalf("ALLOW_PUBLIC=1 must accept public IP, got %v", err)
	}
}

// Register must reject a bad URL before claiming the name (no token, no entry).
func TestRegisterRejectsPublicURL(t *testing.T) {
	t.Setenv("CHANNEL_REGISTER_ALLOW_PUBLIC", "")
	r := New()
	if _, err := r.Register("rogue", "http://8.8.8.8:9001", []string{"x:"}, nil); err == nil {
		t.Fatal("Register must reject a public adapter URL")
	}
	if r.Get("rogue") != nil {
		t.Error("rejected registration must not leave an entry")
	}
}
