package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

func TestSpawnFolderName(t *testing.T) {
	cases := []struct{ parent, jid, want string }{
		{"main", "telegram:123", "main/telegram_123"},
		{"world/support", "discord:98765", "world/support/discord_98765"},
		{"main", "UPPER:ABC", "main/upper_abc"},
	}
	for _, c := range cases {
		got := spawnFolderName(c.parent, c.jid)
		if got != c.want {
			t.Errorf("spawnFolderName(%q,%q) = %q, want %q", c.parent, c.jid, got, c.want)
		}
	}
}

func TestSpawnFromPrototype_NoPrototype(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cfg := &core.Config{GroupsDir: dir}
	gw := &Gateway{cfg: cfg, store: s, groups: make(map[string]core.Group)}

	parentJID := "parent@test"
	parentFolder := "main"
	os.MkdirAll(filepath.Join(dir, parentFolder), 0o755)
	// no prototype/ subdir
	s.PutGroup(parentJID, core.Group{
		JID: parentJID, Name: "main", Folder: parentFolder,
		AddedAt: time.Now(), State: "active",
		Config: core.GroupConfig{MaxChildren: 5},
	})
	gw.groups[parentJID] = s.AllGroups()[parentJID]

	_, err = gw.spawnFromPrototype(parentJID, parentFolder, "child@test")
	if err == nil {
		t.Fatal("expected error for missing prototype dir")
	}
}

func TestSpawnFromPrototype_Success(t *testing.T) {
	dir := t.TempDir()
	s, err := store.OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	cfg := &core.Config{GroupsDir: dir}
	gw := &Gateway{cfg: cfg, store: s, groups: make(map[string]core.Group)}

	parentJID := "parent@test"
	parentFolder := "main"
	protoDir := filepath.Join(dir, parentFolder, "prototype")
	os.MkdirAll(protoDir, 0o755)
	os.WriteFile(filepath.Join(protoDir, "CLAUDE.md"), []byte("# proto"), 0o644)

	parent := core.Group{
		JID: parentJID, Name: "main", Folder: parentFolder,
		AddedAt: time.Now(), State: "active",
		Config: core.GroupConfig{MaxChildren: 5},
	}
	s.PutGroup(parentJID, parent)
	gw.groups[parentJID] = parent

	childJID := "telegram:99999"
	child, err := gw.spawnFromPrototype(parentJID, parentFolder, childJID)
	if err != nil {
		t.Fatalf("spawnFromPrototype: %v", err)
	}

	// child folder should be main/telegram_99999
	wantFolder := "main/telegram_99999"
	if child.Folder != wantFolder {
		t.Errorf("child.Folder = %q, want %q", child.Folder, wantFolder)
	}

	// CLAUDE.md should exist in child dir
	claudePath := filepath.Join(dir, wantFolder, "CLAUDE.md")
	if _, err := os.Stat(claudePath); err != nil {
		t.Errorf("CLAUDE.md not found in child dir: %v", err)
	}

	// logs dir should exist
	logsPath := filepath.Join(dir, wantFolder, "logs")
	if _, err := os.Stat(logsPath); err != nil {
		t.Errorf("logs dir not found in child dir: %v", err)
	}

	// child should be in store
	all := s.AllGroups()
	stored, ok := all[childJID]
	if !ok {
		t.Fatal("child group not found in store")
	}
	if stored.Parent != parentFolder {
		t.Errorf("stored.Parent = %q, want %q", stored.Parent, parentFolder)
	}
	if stored.State != "active" {
		t.Errorf("stored.State = %q, want active", stored.State)
	}

	// child should be in gw.groups
	gw.mu.RLock()
	_, inMem := gw.groups[childJID]
	gw.mu.RUnlock()
	if !inMem {
		t.Error("child group not found in gateway groups map")
	}
}
