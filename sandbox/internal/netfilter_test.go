package internal

import (
	"bytes"
	"log"
	"os"
	"strings"
	"testing"
	"time"
)

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

// A host name in the allowlist gets NO netfilter rule, and nothing else on the
// VM path enforces it — iptables matches addresses, not names, and 6/24 cut
// per-folder egress to one forward proxy per SPAWN, which is arizuko's path and
// not this one. An operator who allowlists api.github.com here has allowlisted
// nothing, and the silence is what made it look like it worked (BUGS L189).
func TestRestoreNetworkRulesSaysAHostNameIsUnenforced(t *testing.T) {
	m := setupTestManager(t)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	meta := &Meta{
		ID: "test-vm-abcdef01", Name: "t", NetIndex: 1, IP: "10.1.0.2",
		AllowList: []string{"api.github.com"},
		CreatedAt: time.Now().UnixMilli(),
	}
	// iptables is absent in the test environment, so an IP entry would error.
	// A host name must not even reach it — that is the point.
	if err := m.RestoreNetworkRules(meta); err != nil {
		t.Fatalf("a host-name-only allowlist errored: %v", err)
	}

	out := logged.String()
	if !strings.Contains(out, "api.github.com") {
		t.Errorf("the unenforced entry was not named:\n%s", out)
	}
	if !strings.Contains(out, "no netfilter rule") {
		t.Errorf("the log does not say the entry is unenforced:\n%s", out)
	}
}

// An entry with no IP on the VM is a no-op, so nothing is claimed about it
// either way — the guard above must not fire for a VM that has no address yet.
func TestRestoreNetworkRulesIsSilentWithoutAnIP(t *testing.T) {
	m := setupTestManager(t)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	meta := &Meta{ID: "test-vm-abcdef02", Name: "t", AllowList: []string{"api.github.com"}}
	if err := m.RestoreNetworkRules(meta); err != nil {
		t.Fatalf("RestoreNetworkRules: %v", err)
	}
	if strings.Contains(logged.String(), "api.github.com") {
		t.Errorf("a VM with no IP reported an unenforced entry:\n%s", logged.String())
	}
}
