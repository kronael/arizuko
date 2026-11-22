package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kronael/arizuko/store"
)

// End-to-end: revoking a token whose JID label contains "--" must delete THAT
// row, not a "/"-collision twin. Under the old encode/decode, revoking
// hook:team/a--b decoded to hook:team/a/b and deleted the wrong token.
func TestRouteTokenRevoke_dashDashLabelExact(t *testing.T) {
	d, routd := splitAdminDash(t, "alice@x")
	for _, jid := range []string{"hook:team/a--b", "hook:team/a/b"} {
		seedRouteToken(t, routd, store.RouteToken{
			JID: jid, OwnerFolder: "team", TargetFolder: "team",
		})
	}
	mux := newMux(d)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/"+url.PathEscape("hook:team/a--b")+"/revoke", "", "alice@x"))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("revoke = %d body=%q", w.Code, w.Body.String())
	}
	if n := countRouteTokens(t, routd, "team", func(tok store.RouteToken) bool {
		return tok.JID == "hook:team/a--b"
	}); n != 0 {
		t.Errorf("target token still present (%d rows) — revoke missed it", n)
	}
	if n := countRouteTokens(t, routd, "team", func(tok store.RouteToken) bool {
		return tok.JID == "hook:team/a/b"
	}); n != 1 {
		t.Errorf("collision twin was wrongly deleted (%d rows, want 1)", n)
	}
}
