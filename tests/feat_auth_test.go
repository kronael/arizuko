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
		if f.routdDB.MessageExists("auth.bad.1") {
			t.Fatal("rejected ingest must not store the row")
		}
	})

	t.Run("folder-bound-reply", func(t *testing.T) {
		f := bootFederation(t)
		if _, err := f.routdDB.PutTurnContext("turn-fb", "demo", "", "slack:T/C/U", "u1", ""); err != nil {
			t.Fatal(err)
		}
		f.authd.grant("user:agent", "messages:send:own_group")
		foreign := f.authd.mintUser(t, "user:agent", "other")
		rec := postBearer(t, f.routdTS.URL, "POST", "/v1/turns/turn-fb/reply", foreign, "fk", routdv1.ReplyRequest{JID: "slack:T/C/U", Text: "leak"})
		if rec.StatusCode != 403 {
			t.Fatalf("foreign folder reply = %d, want 403", rec.StatusCode)
		}
		own := f.authd.mintUser(t, "user:agent", "demo")
		rec2 := postBearer(t, f.routdTS.URL, "POST", "/v1/turns/turn-fb/reply", own, "ok", routdv1.ReplyRequest{JID: "slack:T/C/U", Text: "legit"})
		if rec2.StatusCode != 200 {
			t.Fatalf("own folder reply = %d, want 200", rec2.StatusCode)
		}
	})
}
