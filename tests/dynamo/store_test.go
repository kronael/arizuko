// Package dynamo_test runs the real store over a DynamoDB endpoint, so the port
// is proved by the code daemons call rather than by the driver's own suite.
package dynamo_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/store"
)

const image = "ghcr.io/extenddb/extenddb-dev:0.1.11"

func endpoint(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("starts a container; run it through `make integration`")
	}
	if got := os.Getenv("ARIZUKO_TEST_DYNAMO_ENDPOINT"); got != "" {
		return got
	}
	docker := []string{"docker"}
	if os.Geteuid() != 0 {
		docker = []string{"sudo", "docker"}
	}
	run := func(args ...string) (string, error) {
		full := append(append([]string{}, docker[1:]...), args...)
		out, err := exec.Command(docker[0], full...).CombinedOutput()
		return strings.TrimSpace(string(out)), err
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()

	name := fmt.Sprintf("arizuko-store-dynamo-%d", os.Getpid())
	_, _ = run("rm", "-f", name)
	if out, err := run("run", "-d", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:18080", port), image); err != nil {
		t.Skipf("cannot start %s: %v: %s", image, err, out)
	}
	t.Cleanup(func() { _, _ = run("rm", "-f", name) })

	addr := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
		if err == nil {
			c.Close()
			return addr
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("%s never listened", image)
	return ""
}

// openStore builds the real store over the endpoint, with routd's entities made.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	addr := endpoint(t)
	prefix := fmt.Sprintf("e2e%d_", time.Now().UnixNano())
	d, err := dynstore.Dial(context.Background(), addr, "us-east-1", prefix)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureEntities(ctx, d, store.OwnerRoutd); err != nil {
		t.Fatalf("make routd's entities: %v", err)
	}
	// The version marker is one item per subsystem and lives beside them.
	if err := d.CreateEntity(ctx, store.ScopeEntity); err != nil {
		t.Fatalf("make the scope entity: %v", err)
	}
	// Every audited write carries a record, and audit owns that declaration.
	if err := audit.EnsureEntities(ctx, d); err != nil {
		t.Fatalf("make the audit entities: %v", err)
	}
	return store.NewOver(d)
}

// A group, a route and a grant go in and come back out, through the same
// methods a daemon calls. Nothing here names SQL.
func TestStoreRunsOverDynamo(t *testing.T) {
	s := openStore(t)

	if err := s.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	folders, err := s.GroupFolders(0)
	if err != nil {
		t.Fatalf("GroupFolders: %v", err)
	}
	if len(folders) != 1 || folders[0] != "acme" {
		t.Fatalf("GroupFolders = %v, want [acme]", folders)
	}

	id, err := s.AddRoute(core.Route{Match: "chat_jid=telegram:1", Target: "acme"})
	if err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	if id == 0 {
		t.Fatal("AddRoute answered id 0")
	}
	again, err := s.AddRoute(core.Route{Match: "chat_jid=telegram:1", Target: "acme"})
	if err != nil {
		t.Fatalf("AddRoute(duplicate): %v", err)
	}
	if again != id {
		t.Errorf("the duplicate answered %d, want the rule's own id %d", again, id)
	}

	if err := s.AddACLRow(core.ACLRow{
		Principal: "user:u1", Action: "admin", Scope: "acme", Effect: "allow",
	}); err != nil {
		t.Fatalf("AddACLRow: %v", err)
	}
	rows, err := s.ACLRowsFor([]string{"user:u1"})
	if err != nil {
		t.Fatalf("ACLRowsFor: %v", err)
	}
	found := false
	for _, r := range rows {
		if r.Principal == "user:u1" && r.Scope == "acme" {
			found = true
		}
	}
	if !found {
		t.Errorf("the grant did not read back; %d rows", len(rows))
	}
}

// routd itself, served from the endpoint. The daemon's own door is what a live
// instance opens, so this is the wiring rather than the store under it.
func TestRoutdRunsOverDynamo(t *testing.T) {
	addr := endpoint(t)
	prefix := fmt.Sprintf("rd%d_", time.Now().UnixNano())
	d, err := dynstore.Dial(context.Background(), addr, "us-east-1", prefix)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := store.EnsureEntities(ctx, d, store.OwnerRoutd); err != nil {
		t.Fatalf("make routd's entities: %v", err)
	}
	if err := d.CreateEntity(ctx, store.ScopeEntity); err != nil {
		t.Fatal(err)
	}
	if err := audit.EnsureEntities(ctx, d); err != nil {
		t.Fatal(err)
	}

	rdb := routd.OpenOver(d)

	if err := rdb.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup through routd: %v", err)
	}
	if _, err := rdb.AddRoute(core.Route{Match: "chat_jid=telegram:9", Target: "acme"}); err != nil {
		t.Fatalf("AddRoute through routd: %v", err)
	}
	routes, err := rdb.Routes()
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if len(routes) != 1 || routes[0].Target != "acme" {
		t.Fatalf("Routes = %+v, want one rule targeting acme", routes)
	}
	groups := rdb.AllGroups()
	if _, ok := groups["acme"]; !ok || len(groups) != 1 {
		t.Fatalf("AllGroups = %v, want just acme", groups)
	}
}
