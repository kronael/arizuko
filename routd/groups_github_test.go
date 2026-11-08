package routd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/router"
	"github.com/kronael/arizuko/store"
)

func TestGroupsMCPGitHubIngress(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetSecretKeys([]byte("k1"))
	for _, folder := range []string{"alice/existing", "bob"} {
		if err := db.PutGroup(core.Group{Folder: folder}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.secretStore().AddGitHubSource(store.GitHubSource{Source: "app/bot", OwnerFolder: "bob"}, "bob"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AddRoute(core.Route{Seq: 30, Match: "chat_jid=github:victim/private/pr/*", Target: "bob"}); err != nil {
		t.Fatal(err)
	}
	before := mustRoutes(t, db)
	groupsDir := t.TempDir()
	grantMCPTools(t, db, "alice", "register_group")
	sock := serveGroupsMCP(t, db, "alice", "folder:alice", groupsDir)
	for _, tc := range []struct{ jid, folder string }{
		{"github:victim/private/pr/1", "alice/new"},
		{"telegram:victim/private/pr/*", "alice/existing"},
		{"*:victim/private/pr/?", "alice/pattern"},
	} {
		_, failure := callToolText(t, sock, "register_group", map[string]any{"jid": tc.jid, "folder": tc.folder})
		if !strings.Contains(failure, "operator access required") {
			t.Errorf("registration %q: failure=%q", tc.jid, failure)
		}
		if tc.folder != "alice/existing" && db.GroupExists(tc.folder) {
			t.Errorf("registration %q wrote group", tc.jid)
		}
		if _, err := os.Stat(filepath.Join(groupsDir, tc.folder)); !os.IsNotExist(err) {
			t.Errorf("registration %q wrote files: %v", tc.jid, err)
		}
	}
	if got := mustRoutes(t, db); !reflect.DeepEqual(before, got) {
		t.Fatalf("routes changed: %v", got)
	}
	_, target, ok := router.MatchRoute(core.Message{ChatJID: "github:victim/private/pr/1"}, mustRoutes(t, db))
	if !ok || core.ParseRouteTarget(target).Folder != "bob" {
		t.Fatalf("target=%q ok=%v", target, ok)
	}
	rows, err := db.secretStore().ListGitHubSources("")
	if err != nil || len(rows) != 1 || rows[0].Source != "app/bot" || rows[0].OwnerFolder != "bob" {
		t.Fatalf("sources=%v err=%v", rows, err)
	}
}

func TestGroupsMCPGitHubIngressOperator(t *testing.T) {
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	sock := serveGroupsMCPElevated(t, db, "alice", t.TempDir())
	_, failure := callToolText(t, sock, "register_group", map[string]any{
		"jid": "github:victim/private/pr/1", "folder": "alice/child",
	})
	if failure != "" || !db.GroupExists("alice/child") {
		t.Fatalf("failure=%q group exists=%v", failure, db.GroupExists("alice/child"))
	}
}
