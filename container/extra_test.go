package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

func TestWriteTasksSnapshot_NonRootFilters(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	_ = cfg
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	tasks := []core.Task{
		{ID: "t1", Owner: "atlas"},
		{ID: "t2", Owner: "atlas/support"},
		{ID: "t3", Owner: "other"},
	}
	WriteTasksSnapshot(folders, "atlas", false, tasks)

	ipcDir, _ := folders.IpcPath("atlas")
	data, err := os.ReadFile(filepath.Join(ipcDir, "current_tasks.json"))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	body := string(data)
	if strings.Contains(body, `"t3"`) {
		t.Error("non-owner task leaked into non-root snapshot")
	}
	if !strings.Contains(body, `"t1"`) {
		t.Error("owner task missing from non-root snapshot")
	}
}

func TestWriteGroupsSnapshot_NonRootClearsGroups(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	_ = cfg
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	groups := []core.Group{{Folder: "alpha"}, {Folder: "beta"}}
	WriteGroupsSnapshot(folders, "atlas", false, groups)

	ipcDir, _ := folders.IpcPath("atlas")
	data, err := os.ReadFile(filepath.Join(ipcDir, "available_groups.json"))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	body := string(data)
	if strings.Contains(body, `"alpha"`) || strings.Contains(body, `"beta"`) {
		t.Error("group data leaked into non-root snapshot")
	}
}

func TestWriteGroupsSnapshot_RootKeepsGroups(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	_ = cfg
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	groups := []core.Group{{Folder: "alpha"}, {Folder: "beta"}}
	WriteGroupsSnapshot(folders, "root", true, groups)

	ipcDir, _ := folders.IpcPath("root")
	data, err := os.ReadFile(filepath.Join(ipcDir, "available_groups.json"))
	if err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}
	if !strings.Contains(string(data), `"alpha"`) {
		t.Error("root snapshot missing groups")
	}
}
