package runed

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	runedv1 "github.com/kronael/arizuko/runed/api/v1"
	"github.com/kronael/arizuko/types"
)

// fhsConfig is the shared fixture for the mount-layout tests: a data dir with
// the FHS directories the renderer expects, and its resolver.
func fhsConfig(t *testing.T) (*core.Config, *groupfolder.Resolver) {
	t.Helper()
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir:   filepath.Join(tmp, "groups"),
		IpcDir:      filepath.Join(tmp, "ipc"),
		HostAppDir:  filepath.Join(tmp, "app"),
		WebDir:      filepath.Join(tmp, "web"),
		ProjectRoot: tmp,
	}
	initializeRenderCore(t, cfg)
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	return cfg, &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
}

// findMount returns the mount whose guest path matches, or nil.
func findMount(mounts []core.Mount, guest string) *core.Mount {
	for i, m := range mounts {
		if m.Container == guest {
			return &mounts[i]
		}
	}
	return nil
}

// indexOfMount returns the position of the first mount with the matching
// container path, or -1.
func indexOfMount(mounts []core.Mount, guest string) int {
	for i, m := range mounts {
		if m.Container == guest {
			return i
		}
	}
	return -1
}

// TestBuildMounts_FHSPaths verifies the v0.45.11 FHS rename: platform
// mounts land at canonical paths and per-group web slots are bind-mounted
// from the unified web tree.
func TestBuildMounts_FHSPaths(t *testing.T) {
	cfg, folders := fhsConfig(t)

	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "atlas/support", WebPublish: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)

	cases := []struct {
		container string
		wantRO    bool
		wantHost  string
	}{
		{"/opt/arizuko/ant", true, filepath.Join(cfg.ProjectRoot, "packages", "core")},
		{"/run/ipc", false, ""},                                        // host varies
		{webRoot, true, filepath.Join(cfg.WebDir, "pub")},              // RO whole pub tree
		{filepath.Join(core.ContainerHome, "public_html"), false, ""},  // ~/public_html bind
		{filepath.Join(core.ContainerHome, "private_html"), false, ""}, // ~/private_html bind
		{core.ContainerHome, false, groupDir},                          // group home
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
		req := runedv1.RunRequest{Packages: []string{"core"}, Folder: types.Folder(folder), Elevated: folder == "root"}
		groupDir := filepath.Join(cfg.GroupsDir, folder)
		os.MkdirAll(groupDir, 0o755)
		mounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)
		for _, m := range mounts {
			if strings.HasPrefix(m.Container, "/workspace/") {
				t.Errorf("folder %q: legacy /workspace/ mount %q", folder, m.Container)
			}
		}
	}
}

// The agent's /opt/arizuko must be the staged release, NEVER HOST_APP_DIR.
// Both paths existed and both were called /opt/arizuko: routd and runed read the
// image while the agent — the only path that rewrites a live group's skills —
// read the developer's checkout, so an uncommitted edit migrated the fleet
// (BUGS M1). A same-path assertion alone would not catch a regression here,
// because on a dev box the two directories hold the same bytes.
func TestBuildMounts_AgentSrcIsStagedNotHostCheckout(t *testing.T) {
	cfg, folders := fhsConfig(t)
	cfg.HostAppDir = "/home/dev/checkout"
	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)

	m := findMount(mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir), "/opt/arizuko/ant")
	if m == nil {
		t.Fatal("no /opt/arizuko mount; an agent without it cannot migrate")
	}
	if m.Host == cfg.HostAppDir {
		t.Error("agent mounts HOST_APP_DIR — an uncommitted edit reaches every live group")
	}
	if want := filepath.Join(cfg.ProjectRoot, "packages", "core"); m.Host != want {
		t.Errorf("agent source = %q, want the staged copy %q", m.Host, want)
	}
	if !m.RO {
		t.Error("the staged release must be read-only; an agent may not rewrite it")
	}
}

// ARIZUKO_DEV is documented as bool ("true" or "1"); every test setup and the
// dashd-playwright README spell it "true", so the runner must honor that
// spelling, not just "1" (BUGS F74).
func TestBuildMounts_ArizukoDevAcceptsTrueSpelling(t *testing.T) {
	cfg, folders := fhsConfig(t)
	if err := os.MkdirAll(filepath.Join(cfg.HostAppDir, "ant", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARIZUKO_DEV", "true")

	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)

	if m := findMount(mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir), "/app/src"); m == nil {
		t.Error("ARIZUKO_DEV=true did not mount ant/src; runner accepted only the literal \"1\"")
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

	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "atlas", WebPublish: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)

	wwwIdx := indexOfMount(mounts, webRoot)
	pubIdx := indexOfMount(mounts, filepath.Join(core.ContainerHome, "public_html"))
	privIdx := indexOfMount(mounts, filepath.Join(core.ContainerHome, "private_html"))
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

	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "newgroup", WebPublish: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)

	if _, err := os.Stat(filepath.Join(cfg.WebDir, "pub", "newgroup")); err != nil {
		t.Errorf("expected web/pub/newgroup created: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cfg.WebDir, "priv", "newgroup")); err != nil {
		t.Errorf("expected web/priv/newgroup created: %v", err)
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

	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "root", Elevated: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)

	if m := findMount(mounts, "/var/lib/groups"); m == nil {
		t.Errorf("tier 0 should get /var/lib/groups mount")
	}
}

// The spawn path owns the group's .codex/, so runed needs no startup walk over
// groups/. The walk it replaces derived "group" from a `.claude/` marker on
// disk and so seeded any project directory that had one — root-owned ones
// failed at every restart (BUGS L157). buildMounts runs in-process before
// `docker run`, and a group's spawns are serialised by its run slot, so docker
// never sees a bind source that is about to be created.
func TestBuildMountsOwnsTheGroupCodexDirBeforeDockerSeesIt(t *testing.T) {
	cfg, folders := fhsConfig(t)
	cfg.HostCodexDir = t.TempDir()

	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)

	codex := filepath.Join(groupDir, ".codex")
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatalf(".codex exists before the build; this test checks nothing (err=%v)", err)
	}

	mounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)

	fi, err := os.Stat(codex)
	if err != nil || !fi.IsDir() {
		t.Fatalf("buildMounts did not create %s (err=%v)", codex, err)
	}
	for _, name := range container.CodexROMounts {
		if _, err := os.Stat(filepath.Join(codex, name)); err != nil {
			t.Errorf("overmount target %s not seeded: %v", name, err)
		}
	}
	if findMount(mounts, "/home/node/.codex") == nil {
		t.Error("no .codex mount in the built list")
	}
}

// TestBuildMounts_DocsSubtreeWithoutWebPublish pins the split BUGS L309 asks
// for, in both directions. `seedSkills` gives every folder `ant/skills/arizuko`,
// whose whole job is to answer from `/var/lib/www/arizuko/`, so that subtree
// mounts read-only with no grant. Everything the grant ALSO carries must stay
// behind it: a non-publisher gets no whole-tree view of what other groups
// published — which a group may gate at the edge with an `auth`/`deny`
// web_routes row that a filesystem mount would bypass — and no writable slot.
func TestBuildMounts_DocsSubtreeWithoutWebPublish(t *testing.T) {
	cfg, folders := fhsConfig(t)
	pubHost := filepath.Join(cfg.WebDir, "pub")
	docsHost := filepath.Join(pubHost, webDocsDir)
	os.MkdirAll(docsHost, 0o755)
	// A sibling group's published tree: what the whole-tree mount carries and
	// the docs mount must not.
	os.MkdirAll(filepath.Join(pubHost, "atlas"), 0o755)

	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "nogrant"} // holds no web:publish
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)

	// The grant half first, so a widening still reports when the docs half is
	// also broken.
	if indexOfMount(mounts, webRoot) >= 0 {
		t.Errorf("%s whole-tree mount without web:publish — the docs fix must not widen the grant", webRoot)
	}
	for _, m := range mounts {
		if strings.HasSuffix(m.Container, "/public_html") || strings.HasSuffix(m.Container, "/private_html") {
			t.Errorf("writable web slot %s without web:publish", m.Container)
		}
	}

	docs := filepath.Join(webRoot, webDocsDir)
	i := indexOfMount(mounts, docs)
	if i < 0 {
		t.Fatalf("no %s mount without web:publish; the arizuko skill names a directory that is not there", docs)
	}
	if !mounts[i].RO {
		t.Errorf("%s must be read-only; got a writable mount", docs)
	}
	if mounts[i].Host != docsHost {
		t.Errorf("%s binds %q, want the doc site %q", docs, mounts[i].Host, docsHost)
	}

	// With the grant the whole tree comes back, and the subtree does not mount
	// a second time underneath it.
	req.WebPublish = true
	mounts = mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)
	if indexOfMount(mounts, webRoot) < 0 {
		t.Errorf("web:publish lost the %s whole-tree mount", webRoot)
	}
	if indexOfMount(mounts, docs) >= 0 {
		t.Errorf("%s mounted again under the whole tree it is already part of", docs)
	}
}

func TestBuildMountsCodexCredentials(t *testing.T) {
	cfg, folders := fhsConfig(t)
	cfg.HostCodexDir = t.TempDir()
	for _, n := range container.CodexROMounts {
		if err := os.WriteFile(filepath.Join(cfg.HostCodexDir, n), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	mounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)
	if m := findMount(mounts, "/home/node/.codex"); m == nil || m.RO {
		t.Fatalf("group mount = %+v", m)
	}
	dir := indexOfMount(mounts, "/home/node/.codex")
	uid, _ := container.RunUID()
	for _, n := range container.CodexROMounts {
		i := indexOfMount(mounts, "/home/node/.codex/"+n)
		if i < 0 {
			t.Fatalf("no overmount for %s", n)
		}
		if !mounts[i].RO {
			t.Errorf("overmount %s is writable; a shared credential is read-only", n)
		}
		if mounts[i].Host == filepath.Join(cfg.HostCodexDir, n) {
			t.Errorf("overmount %s binds the operator's own file", n)
		}
		fi, err := os.Stat(mounts[i].Host)
		if err != nil {
			t.Fatal(err)
		}
		if got := int(fi.Sys().(*syscall.Stat_t).Uid); got != uid {
			t.Errorf("staged %s owner = %d, want run uid %d", n, got, uid)
		}
		if fi.Mode().Perm()&0o400 == 0 {
			t.Errorf("staged %s mode %v is not readable by its owner", n, fi.Mode().Perm())
		}
		if dir > i {
			t.Errorf("the group .codex dir (%d) must precede the %s overmount (%d)", dir, n, i)
		}
	}

	cfg.ModelGatewayURL = "http://gateway"
	gatewayMounts := mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)
	if findMount(gatewayMounts, "/home/node/.codex") == nil {
		t.Fatal("gateway lost rollouts")
	}
	for _, n := range container.CodexROMounts {
		if findMount(gatewayMounts, "/home/node/.codex/"+n) != nil {
			t.Fatalf("gateway exposes %s", n)
		}
	}

	// The turn's end removes the copies; the operator's files stay.
	ipcDir, _ := folders.IpcPath(string(req.Folder))
	removeCodexStage(ipcDir)
	for _, n := range container.CodexROMounts {
		if _, err := os.Stat(filepath.Join(ipcDir, codexStagePrefix+n)); !os.IsNotExist(err) {
			t.Errorf("staged %s survives the turn (err=%v)", n, err)
		}
		if _, err := os.Stat(filepath.Join(cfg.HostCodexDir, n)); err != nil {
			t.Errorf("operator file %s gone: %v", n, err)
		}
	}
}

// runed runs as root, so a slot it creates is root-owned unless mkSlot hands it
// to the run uid. A root-owned slot mounts but the agent cannot write it.
func TestMkSlotGivesTheNewSlotToTheRunUID(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pub", "main")
	mkSlot(dir)
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := container.RunUID()
	if got := int(fi.Sys().(*syscall.Stat_t).Uid); got != uid {
		t.Fatalf("slot owner = %d, want the run uid %d", got, uid)
	}
}

func TestBuildMountsNewGroupHomeWritable(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to run as the guest uid")
	}
	cfg, folders := fhsConfig(t)
	for _, dir := range []string{filepath.Dir(cfg.ProjectRoot), cfg.ProjectRoot} {
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	req := runedv1.RunRequest{Packages: []string{"core"}, Folder: "eval/child"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)
	uid, _ := container.RunUID()
	cmd := exec.Command("/usr/bin/touch", filepath.Join(groupDir, "probe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(uid)}}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run uid cannot write group home: %v: %s", err, output)
	}
	if err := os.Chown(groupDir, 0, 0); err != nil {
		t.Fatal(err)
	}
	mustBuildMounts(t, cfg, folders, req, core.GroupConfig{}, groupDir)
	info, err := os.Stat(groupDir)
	if err != nil {
		t.Fatal(err)
	}
	if owner := info.Sys().(*syscall.Stat_t).Uid; owner != 0 {
		t.Fatalf("existing group home owner = %d, want 0", owner)
	}
}
