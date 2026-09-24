package runed

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
)

// turnMounts is the shape buildMounts produces: nine families, one of which is
// the agent's ipc dir and two of which are single files.
func turnMounts() []core.Mount {
	return []core.Mount{
		{Host: "/srv/g/solo/inbox", Container: "/home/node"},
		{Host: "/srv/app-src", Container: "/opt/arizuko", RO: true},
		{Host: "/srv/g/solo/share", Container: "/var/lib/share"},
		{Host: "/srv/ipc/solo/inbox", Container: ipcMount},
		{Host: "/srv/g/solo/inbox/.codex", Container: "/home/node/.codex"},
		{Host: "/root/.codex/auth.json", Container: "/home/node/.codex/auth.json", RO: true},
		{Host: "/srv/web/pub", Container: "/var/lib/www", RO: true},
	}
}

// The tree mirrors the guest's own layout, and the ipc dir is NOT in it.
//
// Both halves are load-bearing. A unix socket does not cross virtio-fs — a
// bind-mounted socket answers "Operation not supported" — so an ipc dir staged
// into the share would give the agent a socket that exists and cannot be dialed,
// which is a turn that makes no tool call and says nothing about why. And the
// order is a parent before its children, because the guest mounts them in this
// order: /home/node/.codex staged before /home/node would be overlaid by it.
func TestTreePlanMirrorsGuestPathsAndKeepsIpcOutOfTheShare(t *testing.T) {
	plan := treePlan(turnMounts())
	want := []treeEntry{
		{Host: "/srv/g/solo/inbox", Path: "/home/node", RO: false},
		{Host: "/srv/g/solo/inbox/.codex", Path: "/home/node/.codex", RO: false},
		{Host: "/root/.codex/auth.json", Path: "/home/node/.codex/auth.json", RO: true},
		{Host: "/srv/app-src", Path: "/opt/arizuko", RO: true},
		{Host: "/srv/g/solo/share", Path: "/var/lib/share", RO: false},
		{Host: "/srv/web/pub", Path: "/var/lib/www", RO: true},
	}
	if !reflect.DeepEqual(plan.Mounts, want) {
		t.Errorf("staging plan\ngot:  %+v\nwant: %+v", plan.Mounts, want)
	}
	if !slices.Equal(plan.Tmpfs, []string{ipcMount}) {
		t.Errorf("the ipc dir must be a guest tmpfs, got tmpfs=%v", plan.Tmpfs)
	}
	for _, e := range plan.Mounts {
		if e.Path == ipcMount {
			t.Fatal("the ipc dir must not reach the share: its socket cannot cross virtio-fs")
		}
	}
}

// Read-only survives the plan. A grant that downgrades a family to ro is the
// whole reason the flag exists, and a tree that dropped it would hand a folder a
// writable copy of what it was granted read.
func TestTreePlanKeepsReadOnly(t *testing.T) {
	plan := treePlan([]core.Mount{
		{Host: "/srv/share", Container: "/var/lib/share", RO: true},
	})
	if len(plan.Mounts) != 1 || !plan.Mounts[0].RO {
		t.Errorf("ro lost in the plan: %+v", plan.Mounts)
	}
}

// A bind target must match its source's kind: the two codex credentials are
// single files overmounted read-only, and a directory target for a file source is
// ENOTDIR at mount time — a failure that would only show up on a host with
// HOST_CODEX_DIR set.
func TestStageTargetsMatchTheSourceKind(t *testing.T) {
	root := t.TempDir()
	hostDir := filepath.Join(root, "home")
	hostFile := filepath.Join(root, "auth.json")
	if err := os.MkdirAll(hostDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hostFile, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := treeFor(filepath.Join(root, "vm"))
	plan := treePlan([]core.Mount{
		{Host: hostDir, Container: "/home/node"},
		{Host: hostFile, Container: "/home/node/.codex/auth.json", RO: true},
	})
	if err := tree.stageTargets(plan); err != nil {
		t.Fatalf("stageTargets: %v", err)
	}
	fi, err := os.Stat(filepath.Join(tree.dir, "home/node"))
	if err != nil || !fi.IsDir() {
		t.Errorf("a directory source needs a directory target: %v", err)
	}
	fi, err = os.Stat(filepath.Join(tree.dir, "home/node/.codex/auth.json"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Errorf("a file source needs a file target: %v", err)
	}
	// The manifest is inside the share, so the guest reads the same declaration
	// the host wrote instead of a second copy on the control channel.
	b, err := os.ReadFile(filepath.Join(tree.dir, treeManifestName))
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	var got treeManifest
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}
	if len(got.Mounts) != 2 || got.Mounts[0].Path != "/home/node" || !got.Mounts[1].RO {
		t.Errorf("manifest does not declare the plan the guest must mount: %+v", got)
	}
	if got.Mounts[0].Host != "" {
		t.Error("the manifest must not name host paths: the guest has no use for them")
	}
}

// A source the renderer named and the host does not have is a refusal. The
// Docker substrate materializes a missing bind source as root, which is the
// failure buildMounts' own mkSlot exists to avoid.
func TestStageTargetsRefusesAMissingSource(t *testing.T) {
	tree := treeFor(t.TempDir())
	err := tree.stageTargets(treePlan([]core.Mount{
		{Host: filepath.Join(t.TempDir(), "gone"), Container: "/home/node"},
	}))
	if err == nil {
		t.Fatal("a missing mount source must refuse rather than be created")
	}
	if !strings.Contains(err.Error(), "/home/node") {
		t.Errorf("the refusal must name the guest path: %v", err)
	}
}

// The removal is what makes the teardown safe rather than merely ordered. A bind
// that is still mounted shows the GROUP'S OWN FILES inside the tree, so a
// recursive delete would delete a folder's home on the host. Anything the tree
// did not create stops it.
func TestTreeRemoveRefusesContentItDidNotCreate(t *testing.T) {
	tree := treeFor(t.TempDir())
	plan := treePlan([]core.Mount{{Host: t.TempDir(), Container: "/home/node"}})
	if err := tree.stageTargets(plan); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(tree.dir, "home/node/PERSONA.md")
	if err := os.WriteFile(live, []byte("# a group's own file\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := tree.remove()
	if err == nil {
		t.Fatal("a tree holding a non-empty file must not be removed")
	}
	if !strings.Contains(err.Error(), "PERSONA.md") {
		t.Errorf("the refusal must name the file it found: %v", err)
	}
	if _, serr := os.Stat(live); serr != nil {
		t.Errorf("the file must still be there: %v", serr)
	}
	// With it gone, the same tree removes cleanly: the refusal is about content,
	// not about the tree being unremovable.
	if err := os.Remove(live); err != nil {
		t.Fatal(err)
	}
	if err := tree.remove(); err != nil {
		t.Fatalf("an emptied tree must remove: %v", err)
	}
	if _, serr := os.Stat(tree.dir); !os.IsNotExist(serr) {
		t.Errorf("the tree dir must be gone: %v", serr)
	}
}

// The teardown reads the live mount table rather than the plan, so a disk scan
// after a runed crash tears down a tree whose plan died with the process that
// made it. Deepest first: a parent cannot unmount while a child is mounted in it.
func TestMountedUnderFindsThisTreesBindsDeepestFirst(t *testing.T) {
	const mountinfo = `25 30 0:22 / /proc rw,nosuid,nodev,noexec,relatime shared:5 - proc proc rw
712 30 253:1 /srv/data/arizuko_krons/groups/solo/inbox /srv/data/arizuko_krons/vms/h1/tree/home/node rw,relatime shared:1 - ext4 /dev/sda1 rw
713 712 253:1 /root/.codex /srv/data/arizuko_krons/vms/h1/tree/home/node/.codex ro,relatime shared:1 - ext4 /dev/sda1 rw
714 30 253:1 /srv/other /srv/data/arizuko_krons/vms/h2/tree/home/node rw,relatime shared:1 - ext4 /dev/sda1 rw
715 30 253:1 /srv/w /srv/data/arizuko_krons/vms/h1/tree/var/lib/my\040share rw,relatime shared:1 - ext4 /dev/sda1 rw
`
	got := mountedUnder(strings.NewReader(mountinfo), "/srv/data/arizuko_krons/vms/h1/tree")
	want := []string{
		"/srv/data/arizuko_krons/vms/h1/tree/home/node/.codex",
		"/srv/data/arizuko_krons/vms/h1/tree/var/lib/my share",
		"/srv/data/arizuko_krons/vms/h1/tree/home/node",
	}
	if !slices.Equal(got, want) {
		t.Errorf("mounts under the tree\ngot:  %v\nwant: %v", got, want)
	}
	// Another guest's tree is not this one's, and a prefix match on the string
	// alone would take it: h1/tree must not match h10/tree either.
	if len(mountedUnder(strings.NewReader(mountinfo), "/srv/data/arizuko_krons/vms/h")) != 0 {
		t.Error("a partial path must not match a mount point")
	}
}
