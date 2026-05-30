package routd

import (
	"encoding/json"
	"net/http"
	"testing"

	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

// fakeVerifier returns a fixed (sub, scope, folder) for any request — stands
// in for offline JWKs verification in the authz tests (mirrors runed's).
type fakeVerifier struct {
	sub    string
	scope  []string
	folder string
}

func (v fakeVerifier) Verify(*http.Request) (string, []string, string, error) {
	return v.sub, v.scope, v.folder, nil
}

func authSrv(t *testing.T, v Verifier) (*DB, http.Handler) {
	t.Helper()
	db, err := OpenMem()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := NewServer(db, nil, &recDeliverer{}, v, 0, "")
	return db, srv.Handler()
}

// TestTurnCallbackRequiresScope: a valid token WITHOUT a send scope is denied
// (403) on a turn callback — the auth-hole fix. Before the fix any verifying
// token could drive any turn.
func TestTurnCallbackRequiresScope(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"chats:read:own_group"}, folder: "demo"})
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "k1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "hi"})
	if rec.Code != 403 {
		t.Fatalf("reply with wrong scope = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if countBots(t, db, "slack:T/C/U") != 0 {
		t.Fatal("denied reply still appended a bot row")
	}
}

// TestTurnCallbackCorrectScope: the agent's own_group send scope, bound to the
// turn's folder, is allowed (200).
func TestTurnCallbackCorrectScope(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"messages:send:own_group"}, folder: "demo"})
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "k1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "hi"})
	if rec.Code != 200 {
		t.Fatalf("reply with correct scope = %d want 200 body=%s", rec.Code, rec.Body.String())
	}
	if countBots(t, db, "slack:T/C/U") != 1 {
		t.Fatal("allowed reply did not append a bot row")
	}
}

// TestTurnCallbackWrongFolder: a token correctly scoped but bound to a DIFFERENT
// folder than the turn's is denied (403) — a token for one group cannot drive
// another group's turn (folder-binding half of the auth-hole fix).
func TestTurnCallbackWrongFolder(t *testing.T) {
	db, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"messages:send:own_group"}, folder: "other"})
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "") // turn owned by "demo"

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "k1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "hi"})
	if rec.Code != 403 {
		t.Fatalf("cross-folder reply = %d want 403 body=%s", rec.Code, rec.Body.String())
	}
	if countBots(t, db, "slack:T/C/U") != 0 {
		t.Fatal("cross-folder reply leaked a bot row")
	}
}

// TestRouteWriteRequiresScope: route CRUD demands routes:write — a read-only
// token is 403, and a write-scoped token bound to a folder cannot target a
// route outside its subtree.
func TestRouteWriteRequiresScope(t *testing.T) {
	// read-only token → 403 on add.
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:read"}, folder: "demo"})
	rec := doJSON(t, h, "POST", "/v1/routes", "", apiv1.Route{Match: "platform=slack", Target: "demo"})
	if rec.Code != 403 {
		t.Fatalf("add with read-only scope = %d want 403", rec.Code)
	}

	// write token bound to "demo" targeting "other" → 403.
	_, h2 := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"routes:write:own_group"}, folder: "demo"})
	rec2 := doJSON(t, h2, "POST", "/v1/routes", "", apiv1.Route{Match: "platform=slack", Target: "other"})
	if rec2.Code != 403 {
		t.Fatalf("add targeting outside subtree = %d want 403", rec2.Code)
	}

	// write token bound to "demo" targeting "demo/sub" → 201 (descendant ok).
	rec3 := doJSON(t, h2, "POST", "/v1/routes", "", apiv1.Route{Match: "platform=slack", Target: "demo/sub"})
	if rec3.Code != 201 {
		t.Fatalf("add targeting own subtree = %d want 201 body=%s", rec3.Code, rec3.Body.String())
	}
}

// TestIngressRequiresWriteScope: POST /v1/messages needs messages:write — a
// token without it is 403.
func TestIngressRequiresWriteScope(t *testing.T) {
	_, h := authSrv(t, fakeVerifier{sub: "user:u", scope: []string{"chats:read"}})
	rec := doJSON(t, h, "POST", "/v1/messages", "",
		apiv1.Message{ChatJID: "slack:T/C/U", Content: "hi"})
	if rec.Code != 403 {
		t.Fatalf("ingress without messages:write = %d want 403", rec.Code)
	}
}

// TestIdemStoreErrorReturns500 is the AppendAndFinish-error fix: when the
// atomic append+ledger-finish commit fails, idem must surface 500 store_error,
// NOT a success body (which would lose the bot row + ledger durability).
func TestIdemStoreErrorReturns500(t *testing.T) {
	db, h := authSrv(t, nil) // nil verifier → open; this isolates the store path
	db.PutTurnContext("t1", "demo", "", "slack:T/C/U", "u1", "")
	// Drop the messages table: IdemClaim (idempotency_keys) still claims the
	// key, but AppendAndFinish's putMessage insert then fails → store_error.
	if _, err := db.SQL().Exec("DROP TABLE messages"); err != nil {
		t.Fatal(err)
	}

	rec := doJSONKey(t, h, "POST", "/v1/turns/t1/reply", "k1",
		apiv1.ReplyRequest{JID: "slack:T/C/U", Text: "hi"})
	if rec.Code != 500 {
		t.Fatalf("reply with failing store = %d want 500 body=%s", rec.Code, rec.Body.String())
	}
	var e apiv1.Err
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil || e.Error != "store_error" {
		t.Fatalf("error=%q want store_error (body=%s)", e.Error, rec.Body.String())
	}
}
