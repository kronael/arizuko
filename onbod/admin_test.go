package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/store"
	_ "modernc.org/sqlite"
)

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

// openOwnedDB (split-mode) creates onbod.db at a fresh path with onbod's OWNED
// tables migrated in — NOT messages.db. The migrator never runs store's
// migrations against it, so a store-only table (e.g. messages) must be absent.
func TestOpenOwnedDB_SplitOpensOnbodDBNotMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()

	// owned tables exist
	for _, tbl := range []string{"onboarding", "invites", "onboarding_gates", "audit_log"} {
		var n int
		if err := db.QueryRow(
			`SELECT 1 FROM sqlite_master WHERE type='table' AND name=?`, tbl).Scan(&n); err != nil {
			t.Errorf("onbod.db missing owned table %q: %v", tbl, err)
		}
	}
	// messages.db-only table must NOT exist (onbod.db is its own file, not messages.db)
	var msgTbl string
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='messages'`).Scan(&msgTbl)
	if err == nil {
		t.Fatalf("onbod.db must not carry the messages table (it is not messages.db)")
	}
}

// admin invite lifecycle through the resreg /v1/invites face (spec 5/16 fold):
// create → list → delete. Drives a real mux so {token} path binding + resreg
// dispatch (a POST-collection create returning a server-generated token) are
// exercised end-to-end; ks=nil (open, monolith/local-dev) so no bearer needed.
func TestAdminInviteCreateListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()
	mux := http.NewServeMux()
	(&admin{db: db, ks: nil}).mountInvites(mux)

	do := func(method, target, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	list := func() []inviteJSON {
		w := do("GET", "/v1/invites", "")
		if w.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
		}
		var listed struct {
			Invites []inviteJSON `json:"invites"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode invites: %v", err)
		}
		return listed.Invites
	}

	// create returns the server-generated bearer ONCE, plus its ref + fields.
	w := do("POST", "/v1/invites", `{"target_glob":"main/","max_uses":3}`)
	if w.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created inviteCreatedJSON
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Token == "" || created.TargetGlob != "main/" || created.MaxUses != 3 {
		t.Fatalf("create returned wrong invite: %+v", created)
	}
	if created.Ref != store.InviteRef(created.Token) {
		t.Fatalf("create ref = %q, want InviteRef(token)", created.Ref)
	}

	// list identifies the invite by ref and NEVER by the bearer — neither as a
	// field nor anywhere in the raw body.
	if inv := list(); len(inv) != 1 || inv[0].Ref != created.Ref {
		t.Fatalf("list = %+v, want the one created invite by ref", inv)
	}
	listBody := do("GET", "/v1/invites", "").Body.String()
	if strings.Contains(listBody, created.Token) {
		t.Fatalf("list leaked the invite bearer: %s", listBody)
	}

	// target_glob is required → 400 (validation before insert, no row added).
	if w := do("POST", "/v1/invites", `{"max_uses":1}`); w.Code != http.StatusBadRequest {
		t.Fatalf("missing target_glob status=%d, want 400", w.Code)
	}

	// the raw bearer is not a delete key — only the ref addresses the row.
	if w := do("DELETE", "/v1/invites/"+created.Token, ""); w.Code != http.StatusNotFound {
		t.Fatalf("delete by raw token status=%d, want 404", w.Code)
	}

	// delete by ref → {ok:true}, then gone.
	if w := do("DELETE", "/v1/invites/"+created.Ref, ""); w.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	if inv := list(); len(inv) != 0 {
		t.Fatalf("after delete list = %+v, want empty", inv)
	}
}

// invitesRESTGate reproduces the retired handlers' scope check: invites:read OR
// invites:write lists; only invites:write mutates (create/revoke). Non-nil ks
// makes it enforce; nil ks is covered by TestAdminInviteCreateListDelete (open).
func TestInvitesRESTGateScopes(t *testing.T) {
	a := &admin{ks: &auth.KeySet{}}
	call := func(action resreg.Action, scopes string) error {
		return a.invitesRESTGate(resreg.Execution{
			Action: action,
			Caller: resreg.Caller{Claims: map[string]string{"scopes": scopes}},
		}, "", nil)
	}
	cases := []struct {
		action resreg.Action
		scopes string
		ok     bool
	}{
		{resreg.ActionList, "invites:read", true},
		{resreg.ActionList, "invites:write", true}, // write covers read
		{resreg.ActionList, "gates:read", false},
		{resreg.ActionCreate, "invites:write", true},
		{resreg.ActionCreate, "invites:read", false}, // read cannot mutate
		{resreg.ActionCreate, "", false},
		{resreg.ActionDelete, "invites:write", true},
		{resreg.ActionDelete, "invites:read", false},
	}
	for _, c := range cases {
		if err := call(c.action, c.scopes); (err == nil) != c.ok {
			t.Errorf("%s scopes=%q: got err=%v, want ok=%v", c.action, c.scopes, err, c.ok)
		}
	}
}

// admin gate lifecycle through the resreg /v1/gates face (spec 5/16 fold):
// put (limit) → list → disable → re-put limit → delete. Drives a real mux so
// {gate} path binding + resreg dispatch are exercised end-to-end; ks=nil (open)
// so no bearer needed.
func TestAdminGatePutListDelete(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store", "onbod.db")
	mustMkdir(t, filepath.Dir(path))
	db, err := openOwnedDB(path)
	if err != nil {
		t.Fatalf("openOwnedDB: %v", err)
	}
	defer db.Close()
	mux := http.NewServeMux()
	(&admin{db: db, ks: nil}).mountGates(mux)

	do := func(method, target, body string) *httptest.ResponseRecorder {
		var rdr *strings.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		} else {
			rdr = strings.NewReader("")
		}
		req := httptest.NewRequest(method, target, rdr)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	list := func() []gateJSON {
		w := do("GET", "/v1/gates", "")
		if w.Code != http.StatusOK {
			t.Fatalf("list status=%d body=%s", w.Code, w.Body.String())
		}
		var listed struct {
			Gates []gateJSON `json:"gates"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &listed); err != nil {
			t.Fatalf("decode gates: %v", err)
		}
		return listed.Gates
	}
	put := func(gate, body string) {
		w := do("PUT", "/v1/gates/"+gate, body)
		if w.Code != http.StatusOK {
			t.Fatalf("put gate %s status=%d body=%s", gate, w.Code, w.Body.String())
		}
	}

	put("github:org=acme", `{"limit_per_day":25}`)
	if g := list(); len(g) != 1 || g[0].Gate != "github:org=acme" ||
		g[0].LimitPerDay != 25 || !g[0].Enabled {
		t.Fatalf("gate list wrong: %+v", g)
	}

	// tri-state: enabled-only PUT flips the flag and leaves the limit untouched.
	put("github:org=acme", `{"enabled":false}`)
	if g := list(); g[0].Enabled || g[0].LimitPerDay != 25 {
		t.Fatalf("enabled-only put should disable but keep limit 25: %+v", g)
	}

	// tri-state: limit-only PUT upserts the limit and leaves enablement untouched.
	put("github:org=acme", `{"limit_per_day":40}`)
	if g := list(); g[0].Enabled || g[0].LimitPerDay != 40 {
		t.Fatalf("limit-only put should keep disabled but set limit 40: %+v", g)
	}

	// delete → {ok:true}, then gone.
	if w := do("DELETE", "/v1/gates/github:org=acme", ""); w.Code != http.StatusOK {
		t.Fatalf("delete gate status=%d body=%s", w.Code, w.Body.String())
	}
	if g := list(); len(g) != 0 {
		t.Fatalf("gate not deleted: %+v", g)
	}
}

// gatesRESTGate reproduces the retired handlers' any-of scope check: gates:read
// OR gates:write lists; only gates:write mutates. Non-nil ks makes it enforce
// (the gate itself never touches ks, so an empty KeySet suffices). nil ks is
// covered by TestAdminGatePutListDelete (open path).
func TestGatesRESTGateScopes(t *testing.T) {
	a := &admin{ks: &auth.KeySet{}}
	call := func(action resreg.Action, scopes string) error {
		return a.gatesRESTGate(resreg.Execution{
			Action: action,
			Caller: resreg.Caller{Claims: map[string]string{"scopes": scopes}},
		}, "", nil)
	}
	cases := []struct {
		action resreg.Action
		scopes string
		ok     bool
	}{
		{resreg.ActionList, "gates:read", true},
		{resreg.ActionList, "gates:write", true}, // write covers read
		{resreg.ActionList, "invites:read", false},
		{resreg.ActionUpdate, "gates:write", true},
		{resreg.ActionUpdate, "gates:read", false}, // read cannot mutate
		{resreg.ActionUpdate, "", false},
		{resreg.ActionDelete, "gates:write", true},
		{resreg.ActionDelete, "gates:read", false},
	}
	for _, c := range cases {
		err := call(c.action, c.scopes)
		if (err == nil) != c.ok {
			t.Errorf("%s scopes=%q: got err=%v, want ok=%v", c.action, c.scopes, err, c.ok)
		}
	}
}
