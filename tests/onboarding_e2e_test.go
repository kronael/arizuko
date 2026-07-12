package tests

// Whole-path chat-initiated onboarding e2e (the regression a real new user hit
// on krons 2026-07-12: first contact → silence → hand-run `arizuko group add`):
//
//	new unrouted inbound over HTTP → routd route-miss → onboarding row lands in
//	the onbod-owned store (via routd's production OnbodClient over HTTP) →
//	operator admits (approve + group/route registration, the `arizuko group
//	add` shape) → the SAME chat's next message routes → the agent replies.
//
// Rides bootFederation: real authd JWKS + adapter tokens, routd's real ingest →
// queue → dispatch path, real runed over HTTP, FakeRuntime as the agent. The
// onbod surface is its real handler body (store.InsertOnboarding on a migrated
// schema) behind httptest — onbod is package main, so its mux can't be imported;
// the store layer it calls IS the imported real code.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/routd"
	routdv1 "github.com/kronael/arizuko/routd/api/v1"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/tests/testutils"
)

func TestOnboardingEndToEnd(t *testing.T) {
	f := bootFederation(t, func(c *routd.LoopConfig) { c.OnboardingEnabled = true })

	// onbod stand-in: POST /v1/onboarding runs the real store insert on a real
	// migrated schema (exactly what onbod's handleOnboardingInsert does after
	// its bearer check).
	onbodInst := testutils.NewInstance(t)
	onbodStore := store.New(onbodInst.DB)
	onbodMux := http.NewServeMux()
	onbodMux.HandleFunc("POST /v1/onboarding", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			JID string `json:"jid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.JID == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if err := onbodStore.InsertOnboarding(body.JID); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	onbodTS := httptest.NewServer(onbodMux)
	t.Cleanup(onbodTS.Close)
	f.loop.SetOnbodClient(routd.NewOnbodClient(onbodTS.URL,
		func(context.Context) (string, error) { return "svc-tok", nil }))

	const jid = "telegram:group/424242"
	f.authd.grant("user:agent", "messages:send:own_group", "chats:read:own_group")

	// 1. First contact: an unrouted chat posts over the real adapter path.
	in := routdv1.Message{ID: "onb.1", ChatJID: jid, Sender: "nikol", Content: "/start hello?", Verb: "message"}
	rec := postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "teled"), "", in)
	if rec.StatusCode != 200 {
		t.Fatalf("ingest status=%d", rec.StatusCode)
	}

	// The route miss must queue admission — the onboarding row appears in the
	// onbod-owned store with status awaiting_message (the dashboard queue +
	// greeting tick both key on it). This is the step that was dead live.
	testutils.WaitForRow(t, onbodInst.DB,
		`SELECT COUNT(*) FROM onboarding WHERE jid=? AND status='awaiting_message'`,
		[]any{jid}, 5*time.Second)

	// No agent turn fired for the unrouted chat.
	if got := f.dispatchedFolder("onb.1"); got != "" {
		t.Fatalf("route miss dispatched a turn to %q", got)
	}
	if n := countBotRows(t, f.routdDB, jid); n != 0 {
		t.Fatalf("route miss produced %d bot rows, want 0", n)
	}

	// 2. Operator admits: approve the queue row, then register the group +
	// route — the `arizuko group add <jid> <folder>` shape (PutGroup + a
	// room= route on the chat's JID).
	if err := onbodStore.ApproveOnboarding(jid); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := f.routdDB.PutGroup(core.Group{Folder: "niki"}); err != nil {
		t.Fatalf("put group: %v", err)
	}
	if _, err := f.routdDB.AddRoute(core.Route{Match: "room=" + core.JidRoom(jid), Target: "niki"}); err != nil {
		t.Fatalf("add route: %v", err)
	}

	// 3. The same chat posts again — now it routes and the agent replies.
	in2 := routdv1.Message{ID: "onb.2", ChatJID: jid, Sender: "nikol", Content: "hello again", Verb: "message"}
	rec = postBearer(t, f.routdTS.URL, "POST", "/v1/messages", f.authd.mintAdapter(t, "teled"), "", in2)
	if rec.StatusCode != 200 {
		t.Fatalf("post-admit ingest status=%d", rec.StatusCode)
	}
	testutils.WaitForRow(t, f.routdDB.SQL(),
		`SELECT COUNT(*) FROM messages WHERE chat_jid=? AND is_bot_message=1`,
		[]any{jid}, 5*time.Second)
	if got := f.dispatchedFolder("onb.2"); got != "niki" {
		t.Fatalf("post-admit turn dispatched to %q, want niki", got)
	}
	testutils.AssertMessage(t, f.routdDB.SQL(), jid, "ack onb.2")
}
