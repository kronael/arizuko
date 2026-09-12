package tests

import (
	"testing"

	"github.com/kronael/arizuko/core"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
)

func TestFeature_Auth(t *testing.T) {
	t.Run("inbound-valid-token", func(t *testing.T) {
		f := bootFederation(t)
		if err := f.routdDB.PutGroup(core.Group{Folder: "demo"}); err != nil {
			t.Fatal(err)
		}
		in := routdv1.Message{ID: "auth.ok.1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "x", Verb: "message"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "teled"), "", in); rec.StatusCode != 200 {
			t.Fatalf("valid token = %d, want 200", rec.StatusCode)
		}
	})

	t.Run("inbound-invalid-token", func(t *testing.T) {
		f := bootFederation(t)
		in := routdv1.Message{ID: "auth.bad.1", ChatJID: "slack:T/C/U", Sender: "u1", Content: "x", Verb: "message"}
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", "not-a-jwt", "", in); rec.StatusCode != 401 {
			t.Fatalf("bogus bearer = %d, want 401", rec.StatusCode)
		}
		weak := f.authd.mintService(t, "service:slakd", "chats:read")
		if rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", weak, "", in); rec.StatusCode != 403 {
			t.Fatalf("wrong-scope token = %d, want 403", rec.StatusCode)
		}
		if messageExists(f.routdDB, "slack:T/C/U", "auth.bad.1") {
			t.Fatal("rejected ingest must not store the row")
		}
	})
}
