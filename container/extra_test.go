package container

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// ---------------------------------------------------------------------------
// worldOf / tierOf
// ---------------------------------------------------------------------------

func TestWorldOf(t *testing.T) {
	cases := []struct {
		folder string
		root   bool
		want   string
	}{
		{"", false, ""},
		{"atlas", false, "atlas"},
		{"atlas/support", false, "atlas"},
		{"atlas/eng/sre", false, "atlas"},
		{"anything", true, ""},
	}
	for _, c := range cases {
		if got := worldOf(c.folder, c.root); got != c.want {
			t.Errorf("worldOf(%q, %v) = %q, want %q", c.folder, c.root, got, c.want)
		}
	}
}

func TestTierOf(t *testing.T) {
	cases := []struct {
		folder string
		root   bool
		want   int
	}{
		{"", false, 0},
		{"atlas", false, 1},
		{"atlas/support", false, 2},
		{"atlas/eng/sre", false, 3},
		{"any", true, 0},
	}
	for _, c := range cases {
		if got := tierOf(c.folder, c.root); got != c.want {
			t.Errorf("tierOf(%q, %v) = %d, want %d", c.folder, c.root, got, c.want)
		}
	}
}

// ---------------------------------------------------------------------------
// PickIP — egress helper (pure, no docker)
// ---------------------------------------------------------------------------

func TestPickIP_ValidSubnet(t *testing.T) {
	subnet := "10.42.7.0/24"
	for i := 0; i < 20; i++ {
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

// ---------------------------------------------------------------------------
// EgressConfig.active()
// ---------------------------------------------------------------------------

func TestEgressConfigActive(t *testing.T) {
	e := EgressConfig{}
	if e.active() {
		t.Error("zero EgressConfig should not be active")
	}
	e.AdminURL = "http://crackbox:3129"
	if e.active() {
		t.Error("still inactive without NetworkPrefix")
	}
	e.NetworkPrefix = "arizuko_krons"
	if e.active() {
		t.Error("still inactive without CrackboxContainer")
	}
	e.CrackboxContainer = "arizuko_crackbox"
	if e.active() {
		t.Error("still inactive without AllowlistFn")
	}
	e.AllowlistFn = func(string) ([]string, error) { return nil, nil }
	if !e.active() {
		t.Error("expected active with all fields set")
	}
}

// ---------------------------------------------------------------------------
// buildArgs — egress path (network + ip injected)
// ---------------------------------------------------------------------------

func TestBuildArgsWithEgress(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	egress := EgressConfig{ProxyURL: "http://crackbox:3128"}
	args := buildArgs(cfg, nil, "c1", egress, "net-atlas", "10.99.5.42")
	joined := strings.Join(args, " ")

	if !strings.Contains(joined, "--network net-atlas") {
		t.Errorf("missing --network: %s", joined)
	}
	if !strings.Contains(joined, "--ip 10.99.5.42") {
		t.Errorf("missing --ip: %s", joined)
	}
	if !strings.Contains(joined, "HTTP_PROXY=http://crackbox:3128") {
		t.Errorf("missing HTTP_PROXY: %s", joined)
	}
	if !strings.Contains(joined, "NO_PROXY=localhost,127.0.0.1,gated,routd,crackbox") {
		t.Errorf("missing NO_PROXY: %s", joined)
	}
}

func TestBuildArgsDefaultProxyURL(t *testing.T) {
	cfg := &core.Config{Timezone: "UTC", Image: "img:latest"}
	// ProxyURL empty → falls back to default
	args := buildArgs(cfg, nil, "c2", EgressConfig{}, "net-x", "10.0.0.5")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "HTTP_PROXY=http://crackbox:3128") {
		t.Errorf("expected default proxy URL: %s", joined)
	}
}

// ---------------------------------------------------------------------------
// hp() — edge: path equals ProjectRoot exactly
// ---------------------------------------------------------------------------

func TestHpPathEqualsProjectRoot(t *testing.T) {
	cfg := &core.Config{
		HostProjectRoot: "/host/inst",
		ProjectRoot:     "/srv/data/inst",
	}
	// local == ProjectRoot: rel="" → join gives HostProjectRoot
	got := hp(cfg, "/srv/data/inst")
	if got != "/host/inst" {
		t.Errorf("hp(local=ProjectRoot) = %q, want /host/inst", got)
	}
}

// ---------------------------------------------------------------------------
// SeedSettings tier 3+ — WEB_PREFIX should be ""
// ---------------------------------------------------------------------------

func TestSeedSettingsTier3NoWebPrefix(t *testing.T) {
	d := t.TempDir()
	cfg := &core.Config{Name: "Bot"}
	in := Input{Folder: "atlas/eng/sre"} // tier 3

	seedSettings(d, cfg, in, false)

	data, err := os.ReadFile(filepath.Join(d, "settings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	env := s["env"].(map[string]any)
	if env["WEB_PREFIX"] != "" {
		t.Errorf("WEB_PREFIX (tier 3) = %v, want \"\"", env["WEB_PREFIX"])
	}
	if env["ARIZUKO_TIER"] != "3" {
		t.Errorf("ARIZUKO_TIER = %v, want 3", env["ARIZUKO_TIER"])
	}
}

// ---------------------------------------------------------------------------
// WriteTasksSnapshot / WriteGroupsSnapshot — non-root filtering
// ---------------------------------------------------------------------------

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
