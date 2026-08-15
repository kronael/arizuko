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
		raw := mintToken(t, db, "web:demo/submissions", "demo", "demo", "")
		row, ok := resolveToken(db, raw)
		if !ok || row.JID != "web:demo/submissions" || row.OwnerFolder != "demo" {
			t.Fatalf("resolve = (%q,%q,%v)", row.JID, row.OwnerFolder, ok)
		}
	})

	// Two tokens for the same JID are independent; revoking one kills all.
	t.Run("multi-token-revoke", func(t *testing.T) {
		db := mustRoutdDB(t)
		raw1 := mintToken(t, db, "web:demo/chat", "demo", "demo", "")
		raw2 := mintToken(t, db, "web:demo/chat", "demo", "demo", "")
		revokeTokens(t, db, "web:demo/chat", "demo")
		for _, raw := range []string{raw1, raw2} {
			if _, ok := resolveToken(db, raw); ok {
				t.Fatalf("token %q should be revoked", raw)
			}
		}
	})
}
