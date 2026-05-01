package internal

import (
	"testing"
	"time"
)

func TestLooksLikeDomain(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"github.com", true},
		{"api.github.com", true},
		{"a.b.c.d.example.org", true},
		{"example-site.com", true},
		{"sub-domain.example-site.co.uk", true},
		// IPs
		{"8.8.8.8", false},
		{"192.168.1.1", false},
		{"10.0.0.0", false},
		// CIDRs
		{"10.0.0.0/8", false},
		{"192.168.0.0/16", false},
		// No dot
		{"localhost", false},
		{"example", false},
		// Regex injection prevention
		{"evil.com$", false},
		{"^attack.com", false},
		{"test.*", false},
		{"(evil).com", false},
		{"evil[.]com", false},
		// Edge cases
		{"", false},
		{".", false},
		{".com", false},
		{"com.", false},
	}

	for _, tt := range tests {
		got := looksLikeDomain(tt.input)
		if got != tt.want {
			t.Errorf("looksLikeDomain(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestLooksLikeIP(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"8.8.8.8", true},
		{"192.168.1.1", true},
		{"10.0.0.1", true},
		{"255.255.255.255", true},
		{"10.0.0.0/8", true},
		{"192.168.0.0/16", true},
		{"172.16.0.0/12", true},
		{"0.0.0.0/0", true},
		{"github.com", false},
		{"example.org", false},
		{"localhost", false},
		{"", false},
		{"not-an-ip", false},
		{"256.1.1.1", false},
		{"1.2.3", false},
		{"10.0.0.0/33", false},
	}

	for _, tt := range tests {
		got := looksLikeIP(tt.input)
		if got != tt.want {
			t.Errorf("looksLikeIP(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestAllowlistPersistence(t *testing.T) {
	m := setupTestManager(t)

	meta := &Meta{
		ID:        "test-vm-12345678",
		Name:      "test",
		NetIndex:  1,
		IP:        "10.1.0.2",
		AllowList: []string{"github.com", "8.8.8.8"},
		AllowAll:  false,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	loaded, err := m.loadMeta(meta.ID)
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if len(loaded.AllowList) != 2 {
		t.Errorf("expected 2 allowlist entries, got %d", len(loaded.AllowList))
	}
	if loaded.AllowList[0] != "github.com" {
		t.Errorf("expected github.com, got %s", loaded.AllowList[0])
	}
	if loaded.AllowList[1] != "8.8.8.8" {
		t.Errorf("expected 8.8.8.8, got %s", loaded.AllowList[1])
	}
	if loaded.AllowAll {
		t.Error("expected AllowAll=false")
	}
}

func TestAllowlistAllowAll(t *testing.T) {
	m := setupTestManager(t)

	meta := &Meta{
		ID:        "test-vm-allowall",
		Name:      "test-allowall",
		NetIndex:  2,
		IP:        "10.1.0.3",
		AllowAll:  true,
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	loaded, err := m.loadMeta(meta.ID)
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if !loaded.AllowAll {
		t.Error("expected AllowAll=true")
	}
}
