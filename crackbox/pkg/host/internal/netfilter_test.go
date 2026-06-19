package internal

import (
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
