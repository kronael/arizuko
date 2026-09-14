package dynamo_test

// env_test.go boots each wired daemon's store the way a live instance does:
// from the ENVIRONMENT, through the daemon's own door, with no file to open.
// The handle path is proved beside it (store_test.go); this is the wiring,
// which is the half that decides whether an instance can be pointed at a
// server at all.

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"testing"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/authd/authstore"
	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanlib/chanstore"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/dashd/dashstore"
	"github.com/kronael/arizuko/emaid/emaistore"
	"github.com/kronael/arizuko/objstore/dynstore"
	"github.com/kronael/arizuko/objstore/dynstore/dyntest"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/routd"
	"github.com/kronael/arizuko/runed"
	"github.com/kronael/arizuko/runed/runstore"
	"github.com/kronael/arizuko/store"
)

// servedEnv points this process's environment at the endpoint, exactly as
// compose generation points a live instance's. Nothing is handed back: what
// these bodies prove is that a daemon's own door reads it.
func servedEnv(t *testing.T) {
	t.Helper()
	cfg := dyntest.Config(t)
	t.Setenv(dynstore.EnvURL, cfg.Endpoint)
	t.Setenv(dynstore.EnvRegion, cfg.Region)
	t.Setenv(dynstore.EnvKeyID, cfg.KeyID)
	t.Setenv(dynstore.EnvSecret, cfg.Secret)
	t.Setenv(dynstore.EnvCA, base64.StdEncoding.EncodeToString(cfg.CA))
	if !store.Served() {
		t.Fatal("the environment names no endpoint, so no door below would take the served branch")
	}
}

// fresh drops everything one owner holds, before the body and after it. Before,
// because a boot that seeds into an EMPTY table would otherwise prove a
// leftover; after, because the endpoint outlives the run.
func fresh(t *testing.T, owner string) {
	t.Helper()
	drop := func() {
		h, err := dynstore.FromEnv(dynstore.Prefix(owner))
		if err != nil {
			t.Fatalf("dial %s: %v", owner, err)
		}
		ctx := context.Background()
		left, err := h.Entities(ctx)
		if err != nil {
			t.Fatalf("read %s's entities: %v", owner, err)
		}
		for _, e := range left {
			if err := h.DropEntity(ctx, e); err != nil {
				t.Fatalf("drop %s.%s: %v", owner, e, err)
			}
		}
	}
	drop()
	t.Cleanup(drop)
}

// noFile is a path that does not exist, so a door that opened it would fail
// loud. Every boot below passes one: the endpoint is what answers.
func noFile(t *testing.T, owner string) string {
	t.Helper()
	return store.OwnerDBPath(filepath.Join(t.TempDir(), "store"), owner)
}

// routd's own door, from the environment: the entities, the floor an instance
// is born with, the reads a daemon makes and the audit record its writes land
// in. The path it is handed does not exist.
func TestRoutdBootsFromTheEnvironment(t *testing.T) {
	servedEnv(t)
	fresh(t, store.OwnerRoutd)

	db, err := routd.OpenAt(noFile(t, store.OwnerRoutd))
	if err != nil {
		t.Fatalf("routd.OpenAt over the endpoint: %v", err)
	}
	defer db.Close() //nolint:errcheck // the test is ending

	if err := db.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	if _, err := db.AddRoute(core.Route{Match: "chat_jid=telegram:1", Target: "acme"}); err != nil {
		t.Fatalf("AddRoute: %v", err)
	}
	routes, err := db.Routes()
	if err != nil {
		t.Fatalf("Routes: %v", err)
	}
	if len(routes) != 1 || routes[0].Target != "acme" {
		t.Fatalf("Routes = %+v, want one rule targeting acme", routes)
	}

	// The floor: without it the instance denies everything and boots healthy.
	rows, err := store.NewOver(db.Obj()).ACLRowsFor([]string{store.RoleOperator})
	if err != nil {
		t.Fatalf("read the floor: %v", err)
	}
	if len(rows) == 0 {
		t.Error("a routd born on the endpoint has no operator grant; every request would be denied")
	}

	// The audit sink: routd's own mutations record through the handle it was
	// opened over, and a sink pointed at a pool would drop every row.
	audit.Init(db.Obj(), "dynamo-test")
	if id := audit.Emit(context.Background(), audit.Event{
		Category: audit.CategorySystem, Action: "daemon.start", Actor: "system",
		Surface: audit.SurfaceGateway, Resource: "daemons/routd", Outcome: audit.OutcomeOK,
	}); id == "" {
		t.Fatal("the audit sink answered no id, so Init found no store")
	}
	recorded, err := audit.Query(context.Background(), db.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit record: %v", err)
	}
	if len(recorded) == 0 {
		t.Error("the audit record is empty; routd's writes would be unattributable")
	}
}

// runed's own door, from the environment: the entities runstore declares and
// the record its audited writes land in.
func TestRunedBootsFromTheEnvironment(t *testing.T) {
	servedEnv(t)
	fresh(t, store.OwnerRuned)

	db, err := runed.OpenAt(noFile(t, store.OwnerRuned))
	if err != nil {
		t.Fatalf("runed.OpenAt over the endpoint: %v", err)
	}
	defer db.Close() //nolint:errcheck // the test is ending

	ctx := context.Background()
	if err := db.CreateSpawn(ctx, runstore.Spawn{RunID: "run_env", Folder: "acme"}); err != nil {
		t.Fatalf("CreateSpawn: %v", err)
	}
	sp, err := db.SpawnByRunID(ctx, "run_env")
	if err != nil {
		t.Fatalf("SpawnByRunID: %v", err)
	}
	if sp.Folder != "acme" {
		t.Errorf("the spawn read back on folder %q, want acme", sp.Folder)
	}
	if _, err := audit.EmitTo(ctx, db.Obj(), audit.Event{
		Category: audit.CategorySystem, Action: "daemon.start", Actor: "system",
		Surface: audit.SurfaceGateway, Resource: "daemons/runed", Outcome: audit.OutcomeOK,
	}); err != nil {
		t.Fatalf("runed's audit record: %v", err)
	}
}

// authd's door, from the environment: store.OwnerDriver answers the endpoint
// and authd holds no pool, so the entities authstore declares are made on it
// and every question authd asks is answered there.
func TestAuthdBootsFromTheEnvironment(t *testing.T) {
	servedEnv(t)
	fresh(t, store.OwnerAuthd)

	d, closeStore, err := store.OwnerDriver(store.OwnerAuthd, noFile(t, store.OwnerAuthd))
	if err != nil {
		t.Fatalf("store.OwnerDriver over the endpoint: %v", err)
	}
	defer closeStore() //nolint:errcheck // the test is ending

	ctx := context.Background()
	for _, entity := range authstore.Entities {
		if err := d.CreateEntity(ctx, entity); err != nil {
			t.Fatalf("make %s: %v", entity, err)
		}
	}
	if err := audit.EnsureEntities(ctx, d); err != nil {
		t.Fatalf("make authd's audit record: %v", err)
	}

	as := authstore.New(d)
	key, err := auth.NewSigningKey("env-kid")
	if err != nil {
		t.Fatal(err)
	}
	if err := as.RotateActiveKey(key, time.Now()); err != nil {
		t.Fatalf("install the signing key: %v", err)
	}
	// The health check's question, which is the store's and not a pool's.
	if err := as.Reachable(); err != nil {
		t.Errorf("authd's health read failed over the endpoint: %v", err)
	}
	keys, err := as.SigningKeysWithMaterial()
	if err != nil {
		t.Fatalf("read the signing keys: %v", err)
	}
	active := ""
	for _, k := range keys {
		if k.Active {
			active = k.Key.Kid
		}
	}
	if active != "env-kid" {
		t.Errorf("the slot names %q, want env-kid", active)
	}
	if err := as.LinkOAuthIdentity("u1", "Alice", "google", "g-1"); err != nil {
		t.Fatalf("link an identity: %v", err)
	}
	if owner, name, _, _, ok := as.OAuthIdentityForSub("google:g-1"); !ok || owner != "u1" || name != "Alice" {
		t.Errorf("the identity read back as %q/%q (found=%v)", owner, name, ok)
	}
}

// onbod's two halves, from the environment: the owner driver its own tables
// live on, and the reader door onto routd's. Its boot door is package main, so
// this is what that door is made of.
func TestOnbodBootsFromTheEnvironment(t *testing.T) {
	servedEnv(t)
	fresh(t, store.OwnerOnbod)
	fresh(t, store.OwnerRoutd)

	d, closeStore, err := store.OwnerDriver(store.OwnerOnbod, noFile(t, store.OwnerOnbod))
	if err != nil {
		t.Fatalf("store.OwnerDriver over the endpoint: %v", err)
	}
	defer closeStore() //nolint:errcheck // the test is ending
	ctx := context.Background()
	if err := store.PrepareOwner(ctx, d, store.OwnerOnbod); err != nil {
		t.Fatalf("prepare onbod's entities: %v", err)
	}
	if err := resreg.EnsureSubsystem(ctx, d, d, resreg.SubsystemOnbod); err != nil {
		t.Fatalf("prepare onbod's version marker: %v", err)
	}

	obst := store.NewOver(d)
	inv, _, err := obst.CreateInvite("acme/", "cli", 3, nil)
	if err != nil {
		t.Fatalf("CreateInvite: %v", err)
	}
	invites, err := obst.ListInvites("")
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 1 || invites[0].Ref != inv.Ref {
		t.Fatalf("ListInvites = %+v, want the one just created", invites)
	}

	// The cross handle refuses while routd has made nothing: a reader that
	// answered here would cross-read zero groups and boot green (BUGS F52).
	if _, err := store.OpenOwner(store.OwnerRoutd, noFile(t, store.OwnerRoutd)); err == nil {
		t.Error("the reader door opened onto an owner that has made nothing")
	}
	rdb, err := routd.OpenAt(noFile(t, store.OwnerRoutd))
	if err != nil {
		t.Fatalf("routd.OpenAt: %v", err)
	}
	defer rdb.Close() //nolint:errcheck // the test is ending
	if err := rdb.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	xst, err := store.OpenOwner(store.OwnerRoutd, noFile(t, store.OwnerRoutd))
	if err != nil {
		t.Fatalf("the reader door after routd's boot: %v", err)
	}
	defer xst.Close() //nolint:errcheck // the test is ending
	folders, err := xst.GroupFolders(0)
	if err != nil {
		t.Fatalf("GroupFolders through the reader: %v", err)
	}
	if len(folders) != 1 || folders[0] != "acme" {
		t.Fatalf("the reader answers %v, want [acme]", folders)
	}
}

// An adapter's door, from the environment: its tables live under its OWN
// service name, and it makes them itself. The data dir it is handed stays
// empty, because a served adapter opens no file.
func TestAnAdapterBootsFromTheEnvironment(t *testing.T) {
	servedEnv(t)
	fresh(t, "teled")

	as, err := chanlib.OpenAdapterStore("teled")
	if err != nil {
		t.Fatalf("chanlib.OpenAdapterStore over the endpoint: %v", err)
	}
	defer as.Close() //nolint:errcheck // the test is ending

	ctx := context.Background()
	claim, err := as.Chan.ClaimDelivery(ctx, "env-key", "telegram:1")
	if err != nil {
		t.Fatalf("ClaimDelivery: %v", err)
	}
	if claim.State != chanstore.ClaimWon {
		t.Fatalf("the first claim is %v, want the win", claim.State)
	}
	if err := as.Chan.RecordDelivered(ctx, "env-key", "p-1"); err != nil {
		t.Fatalf("RecordDelivered: %v", err)
	}
	again, err := as.Chan.ClaimDelivery(ctx, "env-key", "telegram:1")
	if err != nil {
		t.Fatalf("the second claim: %v", err)
	}
	if again.PlatformID != "p-1" {
		t.Errorf("the recorded delivery read back as %q, want p-1", again.PlatformID)
	}

	// Under the adapter's own name and nobody else's.
	h, err := dynstore.FromEnv(dynstore.Prefix("teled"))
	if err != nil {
		t.Fatal(err)
	}
	names, err := h.Entities(ctx)
	if err != nil {
		t.Fatalf("read teled's entities: %v", err)
	}
	if len(names) == 0 {
		t.Error("teled's tables are not under its own service name")
	}
}

// emaid ships entities of its own and makes them on the same store chanlib's
// door answered, which is why that door hands the driver back.
func TestEmaidBootsFromTheEnvironment(t *testing.T) {
	servedEnv(t)
	fresh(t, "emaid")

	as, err := chanlib.OpenAdapterStore("emaid")
	if err != nil {
		t.Fatalf("chanlib.OpenAdapterStore over the endpoint: %v", err)
	}
	defer as.Close() //nolint:errcheck // the test is ending
	ctx := context.Background()
	for _, entity := range emaistore.Entities {
		if err := as.Driver.CreateEntity(ctx, entity); err != nil {
			t.Fatalf("make %s: %v", entity, err)
		}
	}

	st := emaistore.New(as.Driver)
	if err := st.LinkMessageToThread("boot@x.com", "boot", "alice@x.com", "boot@x.com"); err != nil {
		t.Fatalf("LinkMessageToThread: %v", err)
	}
	got, ok := st.Thread("boot")
	if !ok || got.FromAddress != "alice@x.com" {
		t.Errorf("the thread read back as %+v (found=%v)", got, ok)
	}
}

// dashd's two stores, from the environment: the owner whose tables it reads and
// writes, and the optional one behind its invites page. Its boot door is
// package main, so this is what that door is made of — store.OpenOwner per
// owner, and dashd's own storage layer over the handle it answers with.
func TestDashdBootsFromTheEnvironment(t *testing.T) {
	servedEnv(t)
	fresh(t, store.OwnerRoutd)
	fresh(t, store.OwnerOnbod)
	ctx := context.Background()
	routdTarget := noFile(t, store.OwnerRoutd)

	// The probe refuses while routd has made nothing, which is what it is for:
	// an endpoint whose owner never booted answers zero rows to every page, and
	// dashd would render an empty console and report healthy.
	if _, err := store.OpenOwner(store.OwnerRoutd, routdTarget); err == nil {
		t.Error("dashd's door opened onto a routd that has made nothing")
	}
	rdb, err := routd.OpenAt(routdTarget)
	if err != nil {
		t.Fatalf("routd.OpenAt over the endpoint: %v", err)
	}
	defer rdb.Close() //nolint:errcheck // the test is ending
	if err := rdb.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}

	st, err := store.OpenOwner(store.OwnerRoutd, routdTarget)
	if err != nil {
		t.Fatalf("dashd's door onto routd over the endpoint: %v", err)
	}
	defer st.Close() //nolint:errcheck // the test is ending

	// The page layer: every statement dashd runs lives behind it, so this is
	// the read every page makes.
	ds := dashstore.New(st.Obj())
	if ds == nil {
		t.Fatal("dashd's storage layer is nil over the endpoint; every page would banner")
	}
	if n, err := ds.GroupCount(); err != nil || n != 1 {
		t.Fatalf("GroupCount = %d (err=%v), want the one group routd holds", n, err)
	}

	// The audit sink boot points at that same store, and a mutation lands its
	// record in the batch that carries it.
	audit.Init(st.Obj(), "dynamo-test")
	t.Cleanup(func() { audit.Init(nil, "") })
	id, err := ds.NewTaskID()
	if err != nil {
		t.Fatalf("mint a task id: %v", err)
	}
	if err := ds.CreateTask(ctx, dashstore.NewTask{
		ID: id, Owner: "acme", ChatJID: "web:acme", Prompt: "boot", Cron: "0 9 * * *",
	}, audit.Event{
		Category: audit.CategoryMutation, Action: "task.create", Actor: "user:op",
		Surface: audit.SurfaceREST, Resource: "scheduled_tasks/" + id, Outcome: audit.OutcomeOK,
	}); err != nil {
		t.Fatalf("CreateTask through the layer: %v", err)
	}
	if tasks := st.ListTasks("", true); len(tasks) != 1 || tasks[0].ID != id {
		t.Fatalf("the task read back as %+v, want the one just created", tasks)
	}
	recorded, err := audit.Query(ctx, st.Obj(), audit.Filter{Limit: audit.MaxLimit})
	if err != nil {
		t.Fatalf("read the audit record: %v", err)
	}
	if len(recorded) == 0 {
		t.Error("the audit record is empty; every dashboard mutation would be unattributable")
	}

	// The secret keyring: a user secret seals at rest under SECRETS_KEY on the
	// same store, which is the half a handle with no keys refuses outright.
	sec := store.NewOver(st.Obj())
	sec.SetSecretKeys([]byte("dynamo-test-key"))
	if err := sec.PutSecretRow(store.ScopeUser, "github:alice", "GITHUB_TOKEN", "ghp_served"); err != nil {
		t.Fatalf("seal a user secret: %v", err)
	}
	got, err := sec.GetSecret(store.ScopeUser, "github:alice", "GITHUB_TOKEN")
	if err != nil {
		t.Fatalf("read the sealed secret: %v", err)
	}
	if got.Value != "ghp_served" {
		t.Errorf("the sealed secret read back as %q, want ghp_served", got.Value)
	}

	// onbod: the optional profile the invites page reads. Its tables are its
	// own, so they are made the way onbod makes them, and dashd's door onto
	// them refuses until they are there.
	onbodTarget := noFile(t, store.OwnerOnbod)
	if _, err := store.OpenOwner(store.OwnerOnbod, onbodTarget); err == nil {
		t.Error("dashd's door opened onto an onbod that has made nothing")
	}
	d, closeOnbod, err := store.OwnerDriver(store.OwnerOnbod, onbodTarget)
	if err != nil {
		t.Fatalf("store.OwnerDriver for onbod: %v", err)
	}
	defer closeOnbod() //nolint:errcheck // the test is ending
	if err := store.PrepareOwner(ctx, d, store.OwnerOnbod); err != nil {
		t.Fatalf("prepare onbod's entities: %v", err)
	}
	ost, err := store.OpenOwner(store.OwnerOnbod, onbodTarget)
	if err != nil {
		t.Fatalf("dashd's door onto onbod over the endpoint: %v", err)
	}
	defer ost.Close() //nolint:errcheck // the test is ending
	inv, _, err := ost.CreateInvite("acme/", "github:op", 3, nil)
	if err != nil {
		t.Fatalf("CreateInvite through the invites page's store: %v", err)
	}
	invites, err := ost.ListInvites("")
	if err != nil {
		t.Fatalf("ListInvites: %v", err)
	}
	if len(invites) != 1 || invites[0].Ref != inv.Ref {
		t.Fatalf("ListInvites = %+v, want the one just created", invites)
	}
}

// A bot reply is the write every turn ends with, and it is the one message that
// opens its chat and flags nothing: `isRealInbound` is false for it, so the
// chat write carries no attribute at all. A driver that refuses that write
// refuses every reply the agent makes (BUGS L292).
func TestABotReplyLandsOverTheEndpoint(t *testing.T) {
	rdb := routd.OpenOver(routdHandle(t))
	if err := rdb.PutGroup(core.Group{Folder: "acme"}); err != nil {
		t.Fatalf("PutGroup: %v", err)
	}
	reply := core.Message{
		ID: "bot-1", ChatJID: "telegram:9", Sender: "assistant", RoutedTo: "acme",
		Content: "the budget is approved", BotMsg: true, FromMe: true,
		Timestamp: time.Now().UTC(),
	}
	if err := rdb.PutMessage(reply); err != nil {
		t.Fatalf("a bot reply over the endpoint: %v", err)
	}
	rows, err := rdb.MessagesBefore("telegram:9", "", 10)
	if err != nil {
		t.Fatalf("read the chat back: %v", err)
	}
	if len(rows) != 1 || rows[0].Content != reply.Content {
		t.Fatalf("the chat holds %d rows, want the reply: %+v", len(rows), rows)
	}
}
