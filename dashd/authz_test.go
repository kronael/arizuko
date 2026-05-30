package main

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/tests/testutils"
)

// requireUser returns sub when X-User-Sub is set; 401 when missing.
func TestRequireUser(t *testing.T) {
	// set
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("X-User-Sub", "alice@x")
	w := httptest.NewRecorder()
	sub, ok := requireUser(w, r)
	if !ok || sub != "alice@x" {
		t.Errorf("sub=%q ok=%v, want alice@x/true", sub, ok)
	}

	// missing
	r2 := httptest.NewRequest("GET", "/", nil)
	w2 := httptest.NewRecorder()
	if _, ok := requireUser(w2, r2); ok {
		t.Error("missing X-User-Sub should fail")
	}
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w2.Code)
	}
}

// requireSameOrigin: missing Origin passes; matching Origin passes;
// cross-origin is rejected.
func TestRequireSameOrigin(t *testing.T) {
	// missing origin allowed
	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "example.com"
	w := httptest.NewRecorder()
	if !requireSameOrigin(w, r) {
		t.Error("missing Origin should pass")
	}

	// same origin passes
	r2 := httptest.NewRequest("POST", "/", nil)
	r2.Host = "example.com"
	r2.Header.Set("Origin", "https://example.com")
	w2 := httptest.NewRecorder()
	if !requireSameOrigin(w2, r2) {
		t.Error("matching Origin should pass")
	}

	// cross-origin rejected
	r3 := httptest.NewRequest("POST", "/", nil)
	r3.Host = "example.com"
	r3.Header.Set("Origin", "https://attacker.com")
	w3 := httptest.NewRecorder()
	if requireSameOrigin(w3, r3) {
		t.Error("cross-origin should be rejected")
	}
	if w3.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w3.Code)
	}
}

// requireAdmin: caller with admin on scope is admitted;
// caller without admin is denied.
func TestRequireAdmin_Admitted(t *testing.T) {
	inst := testutils.NewInstance(t)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "mygroup", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: inst.DB, dbRW: inst.DB}

	r := httptest.NewRequest("POST", "/dash/groups/mygroup/delete", nil)
	r.Host = "example.com"
	r.Header.Set("X-User-Sub", "alice@x")
	w := httptest.NewRecorder()
	sub, ok := d.requireAdmin(w, r, "mygroup")
	if !ok {
		t.Fatalf("requireAdmin denied, want admitted; status=%d", w.Code)
	}
	if sub != "alice@x" {
		t.Errorf("sub = %q, want alice@x", sub)
	}
}

func TestRequireAdmin_Denied(t *testing.T) {
	inst := testutils.NewInstance(t)
	// stranger has no admin grant
	d := &dash{db: inst.DB, dbRW: inst.DB}

	r := httptest.NewRequest("POST", "/dash/groups/mygroup/delete", nil)
	r.Host = "example.com"
	r.Header.Set("X-User-Sub", "stranger@x")
	w := httptest.NewRecorder()
	if _, ok := d.requireAdmin(w, r, "mygroup"); ok {
		t.Error("requireAdmin should deny without grant")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}

// Operator with ** grant is admitted for any scope.
func TestRequireAdmin_OperatorGrant(t *testing.T) {
	inst := testutils.NewInstance(t)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "op@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: inst.DB, dbRW: inst.DB}

	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "example.com"
	r.Header.Set("X-User-Sub", "op@x")
	w := httptest.NewRecorder()
	if _, ok := d.requireAdmin(w, r, "any/nested/folder"); !ok {
		t.Errorf("operator should be admitted for any scope, got %d", w.Code)
	}
}

// X-User-Groups header (JSON array) folds extra principals in.
func TestRequireAdmin_GroupHeader(t *testing.T) {
	inst := testutils.NewInstance(t)
	// Grant is on "ops" principal; user's groups claim includes "ops".
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "ops", Action: "admin", Scope: "grp", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: inst.DB, dbRW: inst.DB}

	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "example.com"
	r.Header.Set("X-User-Sub", "user@x")
	r.Header.Set("X-User-Groups", `["ops","other"]`)
	w := httptest.NewRecorder()
	if _, ok := d.requireAdmin(w, r, "grp"); !ok {
		t.Errorf("user in ops group should be admitted, got %d", w.Code)
	}
}

// X-User-Sub missing → 401 before any ACL check.
func TestRequireAdmin_NoSub(t *testing.T) {
	d := &dash{}
	r := httptest.NewRequest("POST", "/", nil)
	w := httptest.NewRecorder()
	if _, ok := d.requireAdmin(w, r, "scope"); ok {
		t.Error("should deny with missing X-User-Sub")
	}
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// Cross-origin POST is rejected by requireSameOrigin inside requireAdmin.
func TestRequireAdmin_CrossOriginRejected(t *testing.T) {
	inst := testutils.NewInstance(t)
	if err := inst.Store.AddACLRow(core.ACLRow{
		Principal: "alice@x", Action: "admin", Scope: "**", Effect: "allow",
	}); err != nil {
		t.Fatal(err)
	}
	d := &dash{db: inst.DB, dbRW: inst.DB}

	r := httptest.NewRequest("POST", "/", nil)
	r.Host = "dash.example.com"
	r.Header.Set("X-User-Sub", "alice@x")
	r.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	if _, ok := d.requireAdmin(w, r, "**"); ok {
		t.Error("cross-origin request should be rejected by requireSameOrigin")
	}
	if w.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403", w.Code)
	}
}
