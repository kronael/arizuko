// Web / chat portal feature tests: chat token → web JID binding, the
// primitive that ties anonymous web sessions to a folder's dispatch queue.
package tests

import (
	"testing"
)

func TestFeature_WebChat(t *testing.T) {
	// A web: route token resolves to the bound JID and owner folder.
	t.Run("chat-token-binding", func(t *testing.T) {
		db := mustRoutdDB(t)
		raw, _, err := db.IssueRouteToken("web:demo/submissions", "demo")
		if err != nil {
			t.Fatal(err)
		}
		jid, owner, err := db.ResolveRouteToken(raw)
		if err != nil || jid != "web:demo/submissions" || owner != "demo" {
			t.Fatalf("resolve = (%q,%q,%v)", jid, owner, err)
		}
	})

	// Two tokens for the same JID are independent; revoking one kills all.
	t.Run("multi-token-revoke", func(t *testing.T) {
		db := mustRoutdDB(t)
		raw1, _, _ := db.IssueRouteToken("web:demo/chat", "demo")
		raw2, _, _ := db.IssueRouteToken("web:demo/chat", "demo")
		db.RevokeRouteTokens("web:demo/chat", "demo")
		for _, raw := range []string{raw1, raw2} {
			if _, _, err := db.ResolveRouteToken(raw); err == nil {
				t.Fatalf("token %q should be revoked", raw)
			}
		}
	})
}
