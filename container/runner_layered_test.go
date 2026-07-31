package container

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
)

// findMount returns the volumeMount whose Container path matches, or nil.
func findMount(mounts []volumeMount, container string) *volumeMount {
	for i, m := range mounts {
		if m.Container == container {
			return &mounts[i]
		}
	}
	return nil
}

// indexOfMount returns the position of the first mount with the matching
// container path, or -1.
func indexOfMount(mounts []volumeMount, container string) int {
	for i, m := range mounts {
		if m.Container == container {
			return i
		}
	}
	return -1
}

// TestBuildMounts_FHSPaths verifies the v0.45.11 FHS rename: platform
// mounts land at canonical paths and per-group web slots are bind-mounted
// from the unified web tree.
func TestBuildMounts_FHSPaths(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		WebDir:      filepath.Join(tmp, "web"),
		ProjectRoot: tmp,
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	in := Input{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, in, groupDir, false, folders)

	cases := []struct {
		container string
		wantRO    bool
		wantHost  string
	}{
		{"/opt/arizuko", true, cfg.HostAppDir},
		{"/run/ipc", false, ""},                                          // host varies
		{"/var/lib/www", true, filepath.Join(cfg.WebDir, "pub")},         // RO whole pub tree
		{filepath.Join(containerHome, "public_html"), false, ""},         // ~/public_html bind
		{filepath.Join(containerHome, "private_html"), false, ""},        // ~/private_html bind
		{containerHome, false, groupDir},                                 // group home
	}
	for _, c := range cases {
		m := findMount(mounts, c.container)
		if m == nil {
			t.Errorf("missing mount %q", c.container)
			continue
		}
		if m.RO != c.wantRO {
			t.Errorf("mount %q: RO=%v want %v", c.container, m.RO, c.wantRO)
		}
		if c.wantHost != "" && m.Host != c.wantHost {
			t.Errorf("mount %q: Host=%q want %q", c.container, m.Host, c.wantHost)
		}
	}
}

// TestBuildMounts_NoLegacyWorkspace ensures no /workspace/* paths remain
// in the mount table. Regression guard for the v0.45.11 FHS rename.
func TestBuildMounts_NoLegacyWorkspace(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		WebDir:      filepath.Join(tmp, "web"),
		ProjectRoot: tmp,
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	for _, folder := range []string{"root", "atlas", "atlas/support"} {
		in := Input{Folder: folder}
		groupDir := filepath.Join(cfg.GroupsDir, folder)
		os.MkdirAll(groupDir, 0o755)
		isRoot := folder == "root"
		mounts := buildMounts(cfg, in, groupDir, isRoot, folders)
		for _, m := range mounts {
			if strings.HasPrefix(m.Container, "/workspace/") {
				t.Errorf("folder %q: legacy /workspace/ mount %q", folder, m.Container)
			}
		}
	}
}

// TestBuildMounts_WWWBeforeHomeSlot asserts the RO /var/lib/www mount
// appears in argv BEFORE the home-relative public_html/private_html
// mounts. Docker applies bind mounts in argv order; getting these
// reversed could shadow a slot under the RO whole-tree mount.
func TestBuildMounts_WWWBeforeHomeSlot(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		WebDir:      filepath.Join(tmp, "web"),
		ProjectRoot: tmp,
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	in := Input{Folder: "atlas"}
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, in, groupDir, false, folders)

	wwwIdx := indexOfMount(mounts, "/var/lib/www")
	pubIdx := indexOfMount(mounts, filepath.Join(containerHome, "public_html"))
	privIdx := indexOfMount(mounts, filepath.Join(containerHome, "private_html"))
	if wwwIdx < 0 || pubIdx < 0 || privIdx < 0 {
		t.Fatalf("expected www + public_html + private_html mounts; got www=%d pub=%d priv=%d", wwwIdx, pubIdx, privIdx)
	}
	if wwwIdx > pubIdx || wwwIdx > privIdx {
		t.Errorf("/var/lib/www mount must precede home-slot mounts (www=%d pub=%d priv=%d)", wwwIdx, pubIdx, privIdx)
	}
}

// TestBuildMounts_HomeSlotsCreatesPerGroupDirs ensures the per-group
// web/pub/<folder>/ and web/priv/<folder>/ subdirs are created as a
// side-effect of buildMounts (defensive — onbod/SetupGroup also creates
// them, but the spawn path must not fail when they're missing).
func TestBuildMounts_HomeSlotsCreatesPerGroupDirs(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		WebDir:      filepath.Join(tmp, "web"),
		ProjectRoot: tmp,
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	in := Input{Folder: "newgroup"}
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	os.MkdirAll(groupDir, 0o755)
	buildMounts(cfg, in, groupDir, false, folders)

	if _, err := os.Stat(filepath.Join(cfg.WebDir, "pub", "newgroup")); err != nil {
		t.Errorf("expected web/pub/newgroup created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "priv", "newgroup")); err != nil {
		t.Errorf("expected web/priv/newgroup created: %v", err)
	}
}

// TestBuildMounts_Tier3NoWebSlots verifies tier 3+ gets no /var/lib/www
// and no home web slots.
func TestBuildMounts_Tier3NoWebSlots(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		WebDir:      filepath.Join(tmp, "web"),
		ProjectRoot: tmp,
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	in := Input{Folder: "a/b/c"} // tier 3
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, in, groupDir, false, folders)

	for _, m := range mounts {
		if m.Container == "/var/lib/www" {
			t.Errorf("tier 3 should not get /var/lib/www mount")
		}
		if strings.HasSuffix(m.Container, "/public_html") || strings.HasSuffix(m.Container, "/private_html") {
			t.Errorf("tier 3 should not get home web slot mount: %s", m.Container)
		}
	}
}

// TestSetupGroup_CreatesPerGroupWebSlots verifies SetupGroup pre-creates
// the host-side web/pub/<folder>/ and web/priv/<folder>/ dirs that
// runner.go bind-mounts into the agent home. Spec 5/V.
func TestSetupGroup_CreatesPerGroupWebSlots(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)

	if err := SetupGroup(cfg, "newworld", ""); err != nil {
		t.Fatalf("SetupGroup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "pub", "newworld")); err != nil {
		t.Errorf("expected web/pub/newworld: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "priv", "newworld")); err != nil {
		t.Errorf("expected web/priv/newworld: %v", err)
	}
}

// TestSetupGroup_NestedWebSlots verifies subgroup folders get nested
// dirs preserved in the unified web tree.
func TestSetupGroup_NestedWebSlots(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)

	if err := SetupGroup(cfg, "atlas/support", ""); err != nil {
		t.Fatalf("SetupGroup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "pub", "atlas", "support")); err != nil {
		t.Errorf("expected web/pub/atlas/support: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "priv", "atlas", "support")); err != nil {
		t.Errorf("expected web/priv/atlas/support: %v", err)
	}
}

// TestBuildMounts_RootGroupsMount verifies tier 0 gets /var/lib/groups.
func TestBuildMounts_RootGroupsMount(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		WebDir:      filepath.Join(tmp, "web"),
		ProjectRoot: tmp,
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	folders := &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}

	in := Input{Folder: "root"}
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, in, groupDir, true, folders)

	if m := findMount(mounts, "/var/lib/groups"); m == nil {
		t.Errorf("tier 0 should get /var/lib/groups mount")
	}
}
