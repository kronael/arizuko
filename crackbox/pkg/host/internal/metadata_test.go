package internal

import (
	"os"
	"testing"
	"time"
)

func setupTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager("test", t.TempDir())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestSaveAndLoadMeta(t *testing.T) {
	m := setupTestManager(t)

	meta := &Meta{
		ID:        "test-123",
		Name:      "bold_monet",
		NetIndex:  1,
		IP:        "10.1.1.2",
		CreatedAt: time.Now().UnixMilli(),
	}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	loaded, err := m.loadMeta("test-123")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if loaded == nil {
		t.Fatal("loadMeta returned nil")
	}
	if loaded.ID != meta.ID {
		t.Errorf("ID: got %s, want %s", loaded.ID, meta.ID)
	}
	if loaded.Name != meta.Name {
		t.Errorf("Name: got %s, want %s", loaded.Name, meta.Name)
	}
	if loaded.NetIndex != meta.NetIndex {
		t.Errorf("NetIndex: got %d, want %d", loaded.NetIndex, meta.NetIndex)
	}
}

func TestLoadMetaNotFound(t *testing.T) {
	m := setupTestManager(t)

	meta, err := m.loadMeta("nonexistent")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if meta != nil {
		t.Error("expected nil for nonexistent VM")
	}
}

func TestListAll(t *testing.T) {
	m := setupTestManager(t)

	for i, name := range []string{"vm1", "vm2", "vm3"} {
		meta := &Meta{
			ID:        name,
			Name:      name,
			NetIndex:  i + 1,
			CreatedAt: time.Now().UnixMilli() + int64(i*1000),
		}
		if err := m.saveMeta(meta); err != nil {
			t.Fatalf("saveMeta %s: %v", name, err)
		}
	}

	metas, err := m.listAll()
	if err != nil {
		t.Fatalf("listAll: %v", err)
	}
	if len(metas) != 3 {
		t.Errorf("expected 3 VMs, got %d", len(metas))
	}
	// ordered by CreatedAt DESC
	if metas[0].Name != "vm3" {
		t.Errorf("expected vm3 first, got %s", metas[0].Name)
	}
}

func TestNextNetIndex(t *testing.T) {
	m := setupTestManager(t)

	idx, err := m.nextNetIndex()
	if err != nil {
		t.Fatalf("nextNetIndex: %v", err)
	}
	if idx != 1 {
		t.Errorf("expected 1, got %d", idx)
	}

	meta := &Meta{ID: "test", Name: "test", NetIndex: 1, CreatedAt: time.Now().UnixMilli()}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	idx, err = m.nextNetIndex()
	if err != nil {
		t.Fatalf("nextNetIndex: %v", err)
	}
	if idx != 2 {
		t.Errorf("expected 2, got %d", idx)
	}
}

func TestMarkDeleted(t *testing.T) {
	m := setupTestManager(t)

	meta := &Meta{ID: "del-test", Name: "to_delete", NetIndex: 1, CreatedAt: time.Now().UnixMilli()}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	if err := m.markDeleted("del-test"); err != nil {
		t.Fatalf("markDeleted: %v", err)
	}

	loaded, err := m.loadMeta("del-test")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if loaded.DeletedAt == 0 {
		t.Error("DeletedAt should be set")
	}

	metas, _ := m.listAll()
	for _, v := range metas {
		if v.ID == "del-test" {
			t.Error("deleted VM should not appear in listAll")
		}
	}
}

func TestDetectState(t *testing.T) {
	m := setupTestManager(t)

	meta := &Meta{ID: "state-test", Name: "test", NetIndex: 1}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	// No PID file = stopped
	state := m.detectState(meta)
	if state != VMStateStopped {
		t.Errorf("expected stopped, got %s", state)
	}

	// With deleted_at = deleted
	meta.DeletedAt = time.Now().UnixMilli()
	state = m.detectState(meta)
	if state != VMStateDeleted {
		t.Errorf("expected deleted, got %s", state)
	}
}

func TestCleanupDeleted(t *testing.T) {
	m := setupTestManager(t)

	meta := &Meta{
		ID:        "old-deleted",
		Name:      "old",
		NetIndex:  1,
		CreatedAt: time.Now().Add(-30 * 24 * time.Hour).UnixMilli(),
		DeletedAt: time.Now().Add(-10 * 24 * time.Hour).UnixMilli(),
	}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}
	vmDir := m.vmDir("old-deleted")

	if err := m.cleanupDeleted(7 * 24 * time.Hour); err != nil {
		t.Fatalf("cleanupDeleted: %v", err)
	}

	if _, err := os.Stat(vmDir); !os.IsNotExist(err) {
		t.Error("VM dir should be removed after cleanup")
	}
}

func TestSSHKeyPath(t *testing.T) {
	m := setupTestManager(t)

	meta := &Meta{
		ID:         "ssh-test",
		Name:       "test",
		NetIndex:   1,
		SSHKeyPath: "/tmp/test_key",
		CreatedAt:  time.Now().UnixMilli(),
	}
	if err := m.saveMeta(meta); err != nil {
		t.Fatalf("saveMeta: %v", err)
	}

	loaded, err := m.loadMeta("ssh-test")
	if err != nil {
		t.Fatalf("loadMeta: %v", err)
	}
	if loaded.SSHKeyPath != "/tmp/test_key" {
		t.Errorf("SSHKeyPath: got %q, want /tmp/test_key", loaded.SSHKeyPath)
	}
}
