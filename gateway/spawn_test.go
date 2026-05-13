package gateway

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
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
	gw := &Gateway{cfg: cfg, store: s}

	parentFolder := "main"
	os.MkdirAll(filepath.Join(dir, parentFolder), 0o755)
	s.PutGroup(core.Group{
		Name: "main", Folder: parentFolder,
		AddedAt: time.Now(),
		Config:  core.GroupConfig{MaxChildren: 5},
	})

	_, err = gw.spawnFromPrototype(parentFolder, "child@test")
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
	gw := &Gateway{cfg: cfg, store: s}

	parentFolder := "main"
	protoDir := filepath.Join(dir, parentFolder, "prototype")
	os.MkdirAll(protoDir, 0o755)
	os.WriteFile(filepath.Join(protoDir, "CLAUDE.md"), []byte("# proto"), 0o644)

	parent := core.Group{
		Name: "main", Folder: parentFolder,
		AddedAt: time.Now(),
		Config:  core.GroupConfig{MaxChildren: 5},
	}
	s.PutGroup(parent)

	childJID := "telegram:99999"
	child, err := gw.spawnFromPrototype(parentFolder, childJID)
	if err != nil {
		t.Fatalf("spawnFromPrototype: %v", err)
	}

	wantFolder := "main/telegram_99999"
	if child.Folder != wantFolder {
		t.Errorf("child.Folder = %q, want %q", child.Folder, wantFolder)
	}

	claudePath := filepath.Join(dir, wantFolder, "CLAUDE.md")
	if _, err := os.Stat(claudePath); err != nil {
		t.Errorf("CLAUDE.md not found in child dir: %v", err)
	}

	logsPath := filepath.Join(dir, wantFolder, "logs")
	if _, err := os.Stat(logsPath); err != nil {
		t.Errorf("logs dir not found in child dir: %v", err)
	}

	// child should be in store (keyed by folder)
	all := s.AllGroups()
	stored, ok := all[wantFolder]
	if !ok {
		t.Fatal("child group not found in store")
	}
	if stored.Parent != parentFolder {
		t.Errorf("stored.Parent = %q, want %q", stored.Parent, parentFolder)
	}

	// child should be resolvable via DB
	if _, ok := s.GroupByFolder(wantFolder); !ok {
		t.Error("child group not found in DB")
	}

	// DefaultFolderForJID should map childJID -> childFolder
	if mappedFolder := s.DefaultFolderForJID(childJID); mappedFolder != wantFolder {
		t.Errorf("DefaultFolderForJID(%q) = %q, want %q", childJID, mappedFolder, wantFolder)
	}
}
