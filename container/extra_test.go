package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/substrate"
)

func TestPickIP_ValidSubnet(t *testing.T) {
	subnet := "10.42.7.0/24"
	for range 20 {
		ip, err := PickIP(subnet)
		if err != nil {
			t.Fatalf("PickIP error: %v", err)
		}
		if !strings.HasPrefix(ip, "10.42.7.") {
			t.Errorf("ip %q not in subnet %s", ip, subnet)
		}
		// Must not be .0, .1, .2, or .255
		oct := ip[len("10.42.7."):]
		for _, banned := range []string{"0", "1", "2", "255"} {
			if oct == banned {
				t.Errorf("reserved address returned: %s", ip)
			}
		}
	}
}

func TestPickIP_InvalidSubnet(t *testing.T) {
	if _, err := PickIP("not-a-cidr"); err == nil {
		t.Error("expected error for invalid CIDR")
	}
	// /31 too small
	if _, err := PickIP("10.0.0.0/31"); err == nil {
		t.Error("expected error for /31")
	}
}

func TestPickIP_IPv6Rejected(t *testing.T) {
	if _, err := PickIP("::1/128"); err == nil {
		t.Error("expected error for IPv6")
	}
}

// Effective is the one verdict on egress isolation: off by choice carries a
// reason and no error; an image with any other input missing is an error that
// names every missing input; a full config delivers the declaration.
func TestEffective(t *testing.T) {
	e := EgressConfig{}
	eff, err := e.Effective()
	if err != nil {
		t.Fatalf("isolation off by choice is not an error: %v", err)
	}
	if eff.Delivered.Egress != substrate.EgressNone || eff.Reason == "" {
		t.Errorf("zero config must deliver egress none with a reason, got %+v", eff)
	}
	if eff.Declared != Profile {
		t.Errorf("declared must be the substrate's ceiling, got %+v", eff.Declared)
	}

	e.Image = "arizuko-egress:latest"
	_, err = e.Effective()
	if err == nil {
		t.Fatal("an image with nothing else must be a misconfiguration error")
	}
	for _, want := range []string{"network prefix", "uplink network", "allowlist"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error must name the missing %q: %v", want, err)
		}
	}

	e.NetworkPrefix = "arizuko_krons"
	e.UplinkNetwork = "arizuko_krons_default"
	e.AllowlistFn = func(string) ([]string, error) { return nil, nil }
	eff, err = e.Effective()
	if err != nil {
		t.Fatalf("full config: %v", err)
	}
	if eff.Delivered != Profile || eff.Reason != "" {
		t.Errorf("full config must deliver the declaration with no reason, got %+v", eff)
	}
}

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
