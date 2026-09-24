package runed

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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
	os.MkdirAll(cfg.GroupsDir, 0o755)
	os.MkdirAll(cfg.IpcDir, 0o755)
	os.MkdirAll(filepath.Join(cfg.WebDir, "pub"), 0o755)
	return cfg, &groupfolder.Resolver{GroupsDir: cfg.GroupsDir, IpcDir: cfg.IpcDir}
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

	req := runedv1.RunRequest{Folder: "atlas/support", WebPublish: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)

	cases := []struct {
		container string
		wantRO    bool
		wantHost  string
	}{
		{"/opt/arizuko", true, filepath.Join(cfg.ProjectRoot, container.AppSrcDir)},
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
		req := runedv1.RunRequest{Folder: types.Folder(folder), Elevated: folder == "root"}
		groupDir := filepath.Join(cfg.GroupsDir, folder)
		os.MkdirAll(groupDir, 0o755)
		mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)
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
	req := runedv1.RunRequest{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)

	m := findMount(buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir), "/opt/arizuko")
	if m == nil {
		t.Fatal("no /opt/arizuko mount; an agent without it cannot migrate")
	}
	if m.Host == cfg.HostAppDir {
		t.Error("agent mounts HOST_APP_DIR — an uncommitted edit reaches every live group")
	}
	if want := filepath.Join(cfg.ProjectRoot, container.AppSrcDir); m.Host != want {
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

	req := runedv1.RunRequest{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)

	if m := findMount(buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir), "/app/src"); m == nil {
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

	req := runedv1.RunRequest{Folder: "atlas", WebPublish: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)

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

	req := runedv1.RunRequest{Folder: "newgroup", WebPublish: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)

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

	req := runedv1.RunRequest{Folder: "root", Elevated: true}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)

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

	req := runedv1.RunRequest{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)

	codex := filepath.Join(groupDir, ".codex")
	if _, err := os.Stat(codex); !os.IsNotExist(err) {
		t.Fatalf(".codex exists before the build; this test checks nothing (err=%v)", err)
	}

	mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)

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

	req := runedv1.RunRequest{Folder: "nogrant"} // holds no web:publish
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	os.MkdirAll(groupDir, 0o755)
	mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)

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
	mounts = buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)
	if indexOfMount(mounts, webRoot) < 0 {
		t.Errorf("web:publish lost the %s whole-tree mount", webRoot)
	}
	if indexOfMount(mounts, docs) >= 0 {
		t.Errorf("%s mounted again under the whole tree it is already part of", docs)
	}
}

// The RO overmount hands the guest the HOST file's owner and mode, so an
// operator's own ~/.codex at 0600 is unopenable by the agent's uid and codex
// answers "not logged in" — a permission fault wearing an expired-login face.
// The named set is the certain one: a group-readable file may be reachable
// through a supplementary group, so it is not accused.
func TestUnreadableCodexCredsNamesOnlyTheCertainCase(t *testing.T) {
	me := os.Getuid()
	other := me + 1

	for _, tc := range []struct {
		name string
		mode os.FileMode
		uid  int
		want bool
	}{
		{"operator 0600, another uid", 0o600, other, true},
		{"operator 0600, the owner", 0o600, me, false},
		{"0644 world-readable", 0o644, other, false},
		{"0640 group-readable, unknowable", 0o640, other, false},
		{"0000 unopenable by its owner too", 0o000, me, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, n := range container.CodexROMounts {
				if err := os.WriteFile(filepath.Join(dir, n), []byte("{}"), tc.mode); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(filepath.Join(dir, n), tc.mode); err != nil {
					t.Fatal(err)
				}
			}
			got := unreadableCodexCreds(dir, tc.uid)
			if (len(got) > 0) != tc.want {
				t.Fatalf("named %v, want unreadable=%v", got, tc.want)
			}
			if tc.want && len(got) != len(container.CodexROMounts) {
				t.Fatalf("named %d of %d credentials", len(got), len(container.CodexROMounts))
			}
		})
	}
}

// An absent HOST_CODEX_DIR file is the mount's own failure to report; naming it
// here would put a permission verdict on a file that has no permissions.
func TestUnreadableCodexCredsIgnoresAbsent(t *testing.T) {
	if got := unreadableCodexCreds(t.TempDir(), os.Getuid()+1); len(got) != 0 {
		t.Fatalf("named %v for a directory holding no credentials", got)
	}
}

// buildMounts mounts the credential either way — codex is optional and a turn
// that refused over it would break every turn that never runs codex — so the
// log is the whole surfacing. An unread emission is dead wiring, hence this.
func TestBuildMountsSaysWhenCodexCredsAreUnreachable(t *testing.T) {
	cfg, folders := fhsConfig(t)
	cfg.HostCodexDir = t.TempDir()
	for _, n := range container.CodexROMounts {
		p := filepath.Join(cfg.HostCodexDir, n)
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		// 0000: no uid but root can open it, and container.RunUID() is never root.
		if err := os.Chmod(p, 0o000); err != nil {
			t.Fatal(err)
		}
	}
	req := runedv1.RunRequest{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	logged := captureWarnings(t)
	mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)

	out := logged()
	for _, n := range container.CodexROMounts {
		if !strings.Contains(out, filepath.Join(cfg.HostCodexDir, n)) {
			t.Errorf("%s unreadable and unnamed; log was: %s", n, out)
		}
	}
	if !strings.Contains(out, "not logged in") {
		t.Errorf("the log does not name the symptom the operator sees: %s", out)
	}
	if findMount(mounts, "/home/node/.codex/"+container.CodexROMounts[0]) == nil {
		t.Error("the credential was dropped from the mount list; the log is the surfacing, not a skip")
	}
}

// The group's .codex dir carries the rollouts `codex exec resume` reads, so it
// mounts whether or not shared host credentials exist — a deployment that
// authenticates with OPENAI_API_KEY still resumes. Only the credential
// overmounts are conditional, and the writable group dir must come FIRST:
// bind mounts apply in list order, so a file overmount ahead of its parent
// would be shadowed.
func TestBuildMountsCodexLayerOrderAndConditionality(t *testing.T) {
	cfg, folders := fhsConfig(t)
	req := runedv1.RunRequest{Folder: "atlas/support"}
	groupDir := filepath.Join(cfg.GroupsDir, string(req.Folder))
	if err := os.MkdirAll(groupDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// No host credentials: the group dir mounts, the overmounts do not.
	mounts := buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)
	if m := findMount(mounts, "/home/node/.codex"); m == nil || m.RO {
		t.Fatalf("group .codex mount missing or read-only (%+v) — rollouts would die with the guest", m)
	}
	for _, n := range container.CodexROMounts {
		if findMount(mounts, "/home/node/.codex/"+n) != nil {
			t.Errorf("credential overmount %s must stay gated on HostCodexDir", n)
		}
	}

	// With host credentials: both overmount RO, after the group dir.
	cfg.HostCodexDir = "/host/codex"
	mounts = buildMounts(cfg, folders, req, core.GroupConfig{}, groupDir)
	dir := indexOfMount(mounts, "/home/node/.codex")
	for _, n := range container.CodexROMounts {
		i := indexOfMount(mounts, "/home/node/.codex/"+n)
		if i < 0 {
			t.Fatalf("no overmount for %s", n)
		}
		if !mounts[i].RO {
			t.Errorf("overmount %s is writable; a shared credential is read-only", n)
		}
		if mounts[i].Host != "/host/codex/"+n {
			t.Errorf("overmount %s binds %q, want /host/codex/%s", n, mounts[i].Host, n)
		}
		if dir > i {
			t.Errorf("the group .codex dir (%d) must precede the %s overmount (%d)", dir, n, i)
		}
	}
}
