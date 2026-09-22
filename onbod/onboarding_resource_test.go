package main

// Z3 coverage: the onboarding bearer never appears in a read projection. Since
// onbod 0006 there is no column for it to appear in either — the 5/31-identity-pairing fold left
// token_ref/token_expires inert and they were dropped (BUGS F40) — so what is
// pinned here is that the projection stays token-free and that a row written
// before the reshape still survives the migrate chain.

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/resreg"
	"github.com/kronael/arizuko/resreg/resources"
	"github.com/kronael/arizuko/store"
)

// The registered row type must not carry the credential column at all —
// absent, not merely tagged out, so no later edit can surface it.
func TestOnboardingRowHasNoTokenField(t *testing.T) {
	rt := reflect.TypeFor[resources.OnboardingRow]()
	for i := range rt.NumField() {
		f := rt.Field(i)
		if strings.Contains(strings.ToLower(f.Name), "tokenref") {
			t.Errorf("OnboardingRow.%s exposes the bearer hash", f.Name)
		}
		if f.Tag.Get("db") == "token_ref" || f.Tag.Get("db") == "token" {
			t.Errorf("OnboardingRow.%s maps db column %q", f.Name, f.Tag.Get("db"))
		}
	}
}

// GET /v1/onboarding must carry no token-shaped field at all. Asserted on the
// raw response bytes so a future field rename cannot sneak one back in under a
// different key, and counted first so an empty table cannot pass it vacuously.
func TestOnboardingListLeaksNoToken(t *testing.T) {
	db := testDB(t)
	seedAdmission(t, db, store.ArchiveOnboardingRow{JID: "telegram:1", Status: "awaiting_message"})

	a := &admin{st: db}
	mux := http.NewServeMux()
	a.mountOnboarding(mux)

	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest("GET", "/v1/onboarding", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, "telegram:1") {
		t.Fatalf("fixture is vacuous — the seeded row is not in the response: %s", body)
	}
	if strings.Contains(body, "token") {
		t.Errorf("REST list carries a token-shaped field: %s", body)
	}
}

// The retired hand-rolled handlers are gone; the resreg mount serves the same
// paths with the same scope policy.
func TestOnboardingRESTGateRequiresScope(t *testing.T) {
	db := testDB(t)
	a := &admin{st: db, ks: &auth.KeySet{}}
	err := a.onboardingRESTGate(resreg.Execution{
		Action: resreg.ActionList,
		Caller: resreg.Caller{Claims: map[string]string{resreg.ScopeClaimKey: "gates:read"}},
	}, "", nil)
	if err == nil {
		t.Error("wrong scope was allowed")
	}
	if err := a.onboardingRESTGate(resreg.Execution{
		Action: resreg.ActionList,
		Caller: resreg.Caller{Claims: map[string]string{resreg.ScopeClaimKey: "invites:write"}},
	}, "", nil); err != nil {
		t.Errorf("invites:write refused: %v", err)
	}
}
