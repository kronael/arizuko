package routd

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/groupfolder"
	"github.com/kronael/arizuko/store"
)

// TestBuildGatedFnsSendReply: SendReply is deliver-only — it fans the text out
// to the Deliverer and returns the platform id, matching gated's SendReply. It
// does NOT persist the bot row; that is the ipc tool layer's recordOutbound (not
// invoked when the closure is called directly). The full socket path including
// the persist is covered by TestServeTurnMCP_ReplyOverSocket.
func TestBuildGatedFnsSendReply(t *testing.T) {
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

	fns := srv.buildGatedFns(turnMCP{folder: "demo", topic: "", chatJID: "tg:42", turnID: "t1", trigger: "u1"})
	pid, err := fns.SendReply("tg:42", "answer", "")
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
