package routd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/container"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// TestBuildRoutdFnsSendReply: SendReply is deliver-only — it fans the text out
// to the Deliverer and returns the platform id, matching gated's SendReply. It
// does NOT persist the bot row; that is the ipc tool layer's recordOutbound (not
// invoked when the closure is called directly). The full socket path including
// the persist is covered by TestServeTurnMCP_ReplyOverSocket.
func TestBuildRoutdFnsSendReply(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	deliver := &recDeliverer{pid: "pid-9"}
	srv := NewServer(db, nil, deliver, nil, 0, "")
	if _, err := db.PutTurnContext("t1", "demo", "", "tg:42", "u1", ""); err != nil {
		t.Fatal(err)
	}

	fns := srv.buildRoutdFns(turnMCP{folder: "demo", topic: "", chatJID: "tg:42", turnID: "t1", trigger: "u1"})
	pid, _, err := fns.SendReply("tg:42", "answer", "", "mcp-1")
	if err != nil {
		t.Fatalf("SendReply: %v", err)
	}
	if pid != "pid-9" {
		t.Fatalf("platform id=%q want pid-9", pid)
	}
	if len(deliver.sends) != 1 || deliver.sends[0].text != "answer" {
		t.Fatalf("deliver.sends=%+v want one 'answer'", deliver.sends)
	}
}

// TestScopeRoutes: BUGS A3 — inspect_routing's ListRoutes must scope to the
// caller's folder (own routes + nested `folder#...`/`folder/...` targets),
// see the whole table only as root, and propagate a read error instead of
// swallowing it into an empty list.
func TestScopeRoutes(t *testing.T) {
	all := []core.Route{
		{ID: 1, Target: "world/a"},
		{ID: 2, Target: "world/a#task"},
		{ID: 3, Target: "world/a/sub"},
		{ID: 4, Target: "world/b"},
	}
	scoped := scopeRoutes(func() ([]core.Route, error) { return all, nil })

	got, err := scoped("world/a", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("folder scope: got %d routes, want 3 (ids 1,2,3): %+v", len(got), got)
	}
	for _, r := range got {
		if r.ID == 4 {
			t.Fatalf("unrelated group's route leaked into scoped result: %+v", got)
		}
	}

	root, err := scoped("world/a", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(root) != len(all) {
		t.Fatalf("root scope: got %d routes, want %d (whole table)", len(root), len(all))
	}

	failing := scopeRoutes(func() ([]core.Route, error) { return nil, errors.New("db gone") })
	if _, err := failing("world/a", false); err == nil {
		t.Fatal("expected a DB read error to propagate, got nil")
	}
}

// TestRegisterGroupAddsRoute: the manual register_group path persists the group
// AND adds a room-matched default route + git-inits the group dir (ported from
// gateway.registerGroupIPC). Without the route the group is an unreachable
// orphan — the bug this closes.
func TestRegisterGroupAddsRoute(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	groupsDir := t.TempDir()
	registrationCoreSource(t, groupsDir)
	srv := NewServer(db, nil, nil, nil, 0, "")
	srv.SetDirs(groupsDir, "")
	if err := os.MkdirAll(filepath.Join(groupsDir, "ops"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := srv.registerGroup("telegram:777", core.Group{Folder: "ops"}); err != nil {
		t.Fatalf("registerGroup: %v", err)
	}

	if !db.GroupExists("ops") {
		t.Fatal("group not persisted")
	}
	selected, err := container.ReadPackageSelection(os.Getenv("DATA_DIR"), "")
	receipt, coreFound, rerr := db.InstalledPackage("", "core")
	if err != nil || rerr != nil || len(selected) != 1 || !coreFound ||
		receipt.Source != "ant" || receipt.Revision != selected[0].Digest {
		t.Fatalf("core missing before registration success: %+v %+v %v %v", selected, receipt, err, rerr)
	}
	routes, _ := db.Routes()
	var found bool
	for _, r := range routes {
		if r.Match == "room=777" && r.Target == "ops" {
			found = true
		}
	}
	if !found {
		t.Fatalf("room route not added; routes=%+v", routes)
	}
	if _, err := os.Stat(filepath.Join(groupsDir, "ops", ".git")); err != nil {
		t.Fatalf("group dir not git-inited: %v", err)
	}
}

func TestRegisterGroupCoreFailure(t *testing.T) {
	for _, failure := range []string{"source", "receipt"} {
		t.Run(failure, func(t *testing.T) {
			db := memDB(t)
			server := &Server{db: db, groupsDir: filepath.Join(t.TempDir(), "groups")}
			registrationCoreSource(t, server.groupsDir)
			if failure == "source" {
				t.Setenv("APP_SRC_DIR", t.TempDir())
			} else {
				db.obj = coreReceiptFailure{db.obj}
			}
			err := server.registerGroup("telegram:777", core.Group{Folder: "broken"})
			if err == nil || db.GroupExists("broken") {
				t.Fatalf("core failure published a group: %v", err)
			}
			routes, err := db.Routes()
			if err != nil || len(routes) != 0 {
				t.Fatalf("failed registration published routes: %v %v", routes, err)
			}
			if failure == "receipt" {
				selected, err := container.ReadPackageSelection(os.Getenv("DATA_DIR"), "")
				if err != nil || len(selected) != 1 {
					t.Fatalf("receipt failure did not reach activation: %+v %v", selected, err)
				}
			}
		})
	}
}

type coreReceiptFailure struct {
	objstore.Store
}

func (database coreReceiptFailure) GuardedBatch(ctx context.Context,
	checks []objstore.Check, writes []objstore.Write) (bool, error) {
	for _, write := range writes {
		if write.Entity == store.InstalledPackages {
			return false, errors.New("receipt write refused")
		}
	}
	return database.Store.GuardedBatch(ctx, checks, writes)
}

// TestServeTurnMCPSocketLifecycle: ServeTurnMCP binds the group MCP socket in a
// temp ipc dir; the returned stop removes it.
func TestServeTurnMCPSocketLifecycle(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, nil, 0, "")

	// Non-existent nested dir: routd serves before runed mkdirs, so ServeTurnMCP
	// must create the parent itself (else net.Listen fails on a fresh folder).
	ipcDir := filepath.Join(t.TempDir(), "ipc", "demo")
	stop, err := srv.ServeTurnMCP(turnMCP{folder: "demo", turnID: "t1"}, ipcDir)
	if err != nil {
		t.Fatalf("ServeTurnMCP: %v", err)
	}
	sock := groupfolder.IpcSocket(ipcDir)
	if _, err := os.Stat(sock); err != nil {
		t.Fatalf("socket not bound at %s: %v", sock, err)
	}
	stop()
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Fatalf("socket %s still present after stop (err=%v)", filepath.Base(sock), err)
	}
}

// PutGroup upserts, so a failed route insert on a folder that ALREADY existed
// used to roll back rows this call never created — and the purge is a subtree
// delete, so it took the folder's children with it (BUGS L245).
func TestRegisterGroup_RollsBackOnlyWhatItCreated(t *testing.T) {
	d := memDB(t)
	srv := &Server{db: d, groupsDir: t.TempDir()}
	registrationCoreSource(t, srv.groupsDir)

	// A folder that is already there, with a child of its own.
	if err := d.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutGroup(core.Group{Folder: "acme/eng"}); err != nil {
		t.Fatal(err)
	}
	// Make the route insert fail the way it fails in production: the store
	// cannot answer for that entity.
	dropEntity(t, d, store.Routes)

	const jid = "telegram:group/77"
	if err := srv.registerGroup(jid, core.Group{Folder: "acme"}); err == nil {
		t.Fatal("the duplicate route insert reported success")
	}

	for _, folder := range []string{"acme", "acme/eng"} {
		if !d.GroupExists(folder) {
			t.Errorf("the rollback deleted %s, which this call did not create", folder)
		}
	}
}

// Failed route registration withdraws the newly registered subtree's packages.
func TestRegisterGroupRollbackPackages(t *testing.T) {
	db := memDB(t)
	root := t.TempDir()
	srv := &Server{db: db, groupsDir: filepath.Join(root, "groups")}
	registrationCoreSource(t, srv.groupsDir)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "arizuko.toml"), []byte("name = \"probe\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lock, err := container.LockPackages(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := container.PreparePackage(root, source)
	if err != nil {
		t.Fatal(err)
	}
	for _, folder := range []string{"new", "new/child", "keeper"} {
		if folder != "new" {
			if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
				t.Fatal(err)
			}
		}
		if err := container.SelectPackages(root, folder, []container.PackageSnapshot{snapshot}); err != nil {
			t.Fatal(err)
		}
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, name := range []string{"packages", "package-selections"} {
			if err := filepath.WalkDir(filepath.Join(root, name), func(path string, entry os.DirEntry, err error) error {
				if err != nil || !entry.IsDir() {
					return err
				}
				return os.Chmod(path, 0o755)
			}); err != nil {
				t.Error(err)
			}
		}
	})
	dropEntity(t, db, store.Routes)
	if err := srv.registerGroup("telegram:777", core.Group{Folder: "new"}); err == nil {
		t.Fatal("route failure was hidden")
	}
	for _, folder := range []string{"new", "new/child", "keeper"} {
		selected, err := container.ReadPackageSelection(root, folder)
		want := 0
		if folder == "keeper" {
			want = 1
		}
		if err != nil || len(selected) != want {
			t.Fatalf("selection %s = %v %v", folder, selected, err)
		}
		if db.GroupExists(folder) != (folder == "keeper") {
			t.Fatalf("unexpected group state for %s", folder)
		}
	}
}

func TestRegisterGroupRollbackPendingAdoption(t *testing.T) {
	for _, pending := range []string{"core", "group", "malformed", "instance"} {
		t.Run(pending, func(t *testing.T) {
			db := memDB(t)
			root := t.TempDir()
			srv := &Server{db: db, groupsDir: filepath.Join(root, "groups")}
			registrationCoreSource(t, srv.groupsDir)
			cfg := &core.Config{ProjectRoot: root, GroupsDir: srv.groupsDir, AppSrcDir: os.Getenv("APP_SRC_DIR")}
			lock, err := container.LockPackages(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := container.EnsureCorePackage(cfg, nil); err != nil {
				t.Fatal(err)
			}
			selected, err := container.ReadPackageSelection(root, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, folder := range []string{"new", "new/child", "keeper"} {
				if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
					t.Fatal(err)
				}
				if err := container.SelectPackages(root, folder, selected); err != nil {
					t.Fatal(err)
				}
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			row := store.InstalledPackage{Folder: "new/child", Name: "waiting", Source: "origin", Revision: "revision",
				AssetHashes: map[string]string{"pending:adopt:source": strings.Repeat("a", 64),
					"pending:adopt:package_source":                             strings.Repeat("b", 64),
					"pending:adopt:tree:groups/new/child/.claude/skills/probe": strings.Repeat("c", 64),
					container.PendingSettingPrefix + "KEEP":                    strings.Repeat("d", 64)}}
			switch pending {
			case "core":
				row.Folder, row.Name = "", "core"
			case "malformed":
				row.Folder, row.AssetHashes = "keeper", map[string]string{"pending:adopt:unknown": "invalid"}
			case "instance":
				row.Folder = "keeper"
				row.AssetHashes["pending:adopt:tree:skills/probe"] = strings.Repeat("e", 64)
			}
			if err := db.PutInstalledPackage(row); err != nil {
				t.Fatal(err)
			}
			if err := srv.rollbackGroup("new"); err == nil || !strings.Contains(err.Error(), "pending package adoption") {
				t.Fatalf("rollback bypassed adoption: %v", err)
			}
			for _, folder := range []string{"new", "new/child", "keeper"} {
				if !db.GroupExists(folder) {
					t.Fatalf("refusal purged %s", folder)
				}
				got, err := container.ReadPackageSelection(root, folder)
				if err != nil || !reflect.DeepEqual(got, selected) {
					t.Fatalf("refusal withdrew %s: %+v, %v", folder, got, err)
				}
			}
			got, found, err := db.InstalledPackage(row.Folder, row.Name)
			if err != nil || !found || !reflect.DeepEqual(got.AssetHashes, row.AssetHashes) {
				t.Fatalf("refusal changed adoption/settings: %+v, %v", got, err)
			}
		})
	}
}
