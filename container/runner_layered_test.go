package container

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
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

// fhsConfig is the shared fixture for the mount-layout tests: a data dir with
// the FHS directories the runner expects, and its resolver.
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
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	return cfg, &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
}

// TestBuildMounts_FHSPaths verifies the v0.45.11 FHS rename: platform
// mounts land at canonical paths and per-group web slots are bind-mounted
// from the unified web tree.
func TestBuildMounts_FHSPaths(t *testing.T) {
	cfg, folders := fhsConfig(t)

	in := Input{Folder: "atlas/support", WebPublish: true}
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, in, groupDir, false, folders)

	cases := []struct {
		container string
		wantRO    bool
		wantHost  string
	}{
		{"/opt/arizuko", true, filepath.Join(cfg.ProjectRoot, AppSrcDir)},
		{"/run/ipc", false, ""},                                   // host varies
		{"/var/lib/www", true, filepath.Join(cfg.WebDir, "pub")},  // RO whole pub tree
		{filepath.Join(containerHome, "public_html"), false, ""},  // ~/public_html bind
		{filepath.Join(containerHome, "private_html"), false, ""}, // ~/private_html bind
		{containerHome, false, groupDir},                          // group home
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

// The agent's /opt/arizuko must be the staged release, NEVER HOST_APP_DIR.
// Both paths existed and both were called /opt/arizuko: routd and runed read the
// image while the agent — the only path that rewrites a live group's skills —
// read the developer's checkout, so an uncommitted edit migrated the fleet
// (BUGS M1). A same-path assertion alone would not catch a regression here,
// because on a dev box the two directories hold the same bytes.
func TestBuildMounts_AgentSrcIsStagedNotHostCheckout(t *testing.T) {
	cfg, folders := fhsConfig(t)
	cfg.HostAppDir = "/home/dev/checkout"
	in := Input{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, in.Folder)
	os.MkdirAll(groupDir, 0o755)

	m := findMount(buildMounts(cfg, in, groupDir, false, folders), "/opt/arizuko")
	if m == nil {
		t.Fatal("no /opt/arizuko mount; an agent without it cannot migrate")
	}
	if m.Host == cfg.HostAppDir {
		t.Error("agent mounts HOST_APP_DIR — an uncommitted edit reaches every live group")
	}
	if want := filepath.Join(cfg.ProjectRoot, AppSrcDir); m.Host != want {
		t.Errorf("agent source = %q, want the staged copy %q", m.Host, want)
	}
	if !m.RO {
		t.Error("the staged release must be read-only; an agent may not rewrite it")
	}
}

// MaterializeAppSrc is what puts bytes at that staged path. A bind mount reads
// the host filesystem, so a copy that silently no-ops leaves every agent with an
// empty /opt/arizuko and no way to migrate.
func TestMaterializeAppSrc_StagesTheImageCopy(t *testing.T) {
	cfg, _ := fhsConfig(t)
	src := filepath.Join(cfg.ProjectRoot, "opt-arizuko")
	if err := os.MkdirAll(filepath.Join(src, "ant", "skills", "self"), 0o755); err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(src, "ant", "skills", "self", "MIGRATION_VERSION")
	if err := os.WriteFile(version, []byte("199\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.AppSrcDir = src

	dst := MaterializeAppSrc(cfg)
	got, err := os.ReadFile(filepath.Join(dst, "ant", "skills", "self", "MIGRATION_VERSION"))
	if err != nil {
		t.Fatalf("staged copy unreadable: %v", err)
	}
	if string(got) != "199\n" {
		t.Errorf("staged MIGRATION_VERSION = %q, want the source's", got)
	}

	// Overwrite, not merge: the image is the only writer here.
	if err := os.WriteFile(version, []byte("200\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	MaterializeAppSrc(cfg)
	got, _ = os.ReadFile(filepath.Join(dst, "ant", "skills", "self", "MIGRATION_VERSION"))
	if string(got) != "200\n" {
		t.Errorf("restage left %q; a stale copy freezes every group's skills", got)
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

	in := Input{Folder: "atlas", WebPublish: true}
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

	in := Input{Folder: "newgroup", WebPublish: true}
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

// --- BUGS F62: the per-group web slots are chowned where they are created ---

// canChownToContainerUID reports whether this process can hand a file it owns
// to containerUID. Only root can change an owner to a different uid; a process
// already running as containerUID may "change" it to itself.
func canChownToContainerUID() bool {
	return os.Geteuid() == 0 || os.Geteuid() == containerUID
}

// captureWarnings redirects the default slog to a buffer for the test's
// duration and returns the accumulated text.
func captureWarnings(t *testing.T) func() string {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf.String
}

// TestChownR_ReportsFailure locks the mechanism behind F62: chownR used to
// discard every os.Lchown error, so runed (uid 1000) chowning a root-owned
// path failed silently and a read-only ~/public_html persisted for months.
// The error must now come back to the caller.
func TestChownR_ReportsFailure(t *testing.T) {
	if canChownToContainerUID() {
		t.Skipf("euid %d can chown to %d; EPERM is unreachable here", os.Geteuid(), containerUID)
	}
	dir := t.TempDir()
	if err := chownR(dir, containerUID, containerUID); err == nil {
		t.Fatal("chownR discarded the Lchown failure (want the EPERM returned)")
	}
}

// TestSetupGroup_ChownsWebSlots is F62 itself: SetupGroup pre-creates
// web/pub/<folder> and web/priv/<folder>, but the only chown in the path
// (seedGroupDir's) walks GroupsDir — a different tree — so the slots were never
// touched. Assert the chown now reaches both slot paths: by ownership where
// this process can chown, by the loud failure where it cannot.
func TestSetupGroup_ChownsWebSlots(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)

	logged := captureWarnings(t)
	if err := SetupGroup(cfg, "slotted", ""); err != nil {
		t.Fatalf("SetupGroup: %v", err)
	}

	slots := []string{
		filepath.Join(cfg.WebDir, "pub", "slotted"),
		filepath.Join(cfg.WebDir, "priv", "slotted"),
	}
	for _, slot := range slots {
		if canChownToContainerUID() {
			st, err := os.Stat(slot)
			if err != nil {
				t.Fatalf("stat %s: %v", slot, err)
			}
			sys, ok := st.Sys().(*syscall.Stat_t)
			if !ok {
				t.Skip("no syscall.Stat_t on this platform")
			}
			if int(sys.Uid) != containerUID {
				t.Errorf("%s owned by uid %d, want %d — the slot was never chowned",
					slot, sys.Uid, containerUID)
			}
			continue
		}
		// Cannot chown here, so the proof that the chown was ATTEMPTED on the
		// slot is the warning naming it. Silence is the F62 bug.
		if !strings.Contains(logged(), slot) {
			t.Errorf("no chown warning naming %s; the slot is still skipped silently", slot)
		}
	}
}

// TestSetupGroup_WebSlotMkdirIsFatal: an uncreatable slot must fail group
// creation, not pass silently. Left silent, the docker daemon materializes the
// missing bind source as root at spawn and the agent gets an unwritable
// ~/public_html — the symptom F63 shows an operator as "broken mount".
func TestSetupGroup_WebSlotMkdirIsFatal(t *testing.T) {
	tmp := t.TempDir()
	cfg := &core.Config{
		GroupsDir: filepath.Join(tmp, "groups"),
		IpcDir:    filepath.Join(tmp, "ipc"),
		WebDir:    filepath.Join(tmp, "web"),
	}
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	// A regular file where the "pub" directory belongs: MkdirAll under it
	// fails with ENOTDIR.
	os.MkdirAll(cfg.WebDir, 0o755)
	if err := os.WriteFile(filepath.Join(cfg.WebDir, "pub"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := SetupGroup(cfg, "blocked", ""); err == nil {
		t.Fatal("SetupGroup ignored an uncreatable web slot (want an error)")
	}
}
