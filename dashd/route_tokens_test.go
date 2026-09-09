package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kronael/arizuko/store"
)

// encodeJID must be reversible via standard URL unescaping (which Go's mux does
// for the {jid} path segment). The old "/"→"--" scheme collided on labels
// containing "--" (labels allow "-"), so encode(x) round-tripped to a different
// JID and could revoke the wrong token.
func TestEncodeJID_roundTrip(t *testing.T) {
	for _, jid := range []string{
		"web:team",
		"hook:team/github",
		"hook:team/a-b",
		"hook:team/a--b",  // the "--" case the old scheme corrupted
		"hook:corp/eng/x", // multi-segment folder
		"hook:team/a--b--c",
	} {
		got, err := url.PathUnescape(encodeJID(jid))
		if err != nil {
			t.Errorf("PathUnescape(encodeJID(%q)) error: %v", jid, err)
			continue
		}
		if got != jid {
			t.Errorf("round-trip %q -> %q -> %q", jid, encodeJID(jid), got)
		}
	}
}

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
	mux.ServeHTTP(w, adminReq("POST", "/dash/tokens/team/"+encodeJID("hook:team/a--b")+"/revoke", "", "alice@x"))
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
