package routd

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
)

// holdAdapter is a channel that advertises `buttons` and records what routd
// actually put on the wire. It stands in for teled/discd/slakd, whose own tests
// cover the render; this one covers everything BEFORE the render.
type holdAdapter struct {
	mu   sync.Mutex
	sent []map[string]any
	srv  *httptest.Server
}

func newHoldAdapter(t *testing.T, caps map[string]bool) (*holdAdapter, *chanreg.Registry) {
	t.Helper()
	a := &holdAdapter{}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		json.Unmarshal(body, &m)
		a.mu.Lock()
		a.sent = append(a.sent, m)
		a.mu.Unlock()
		w.Write([]byte(`{"id":"platform-1"}`))
	}))
	t.Cleanup(a.srv.Close)

	reg := chanreg.New()
	if _, err := reg.Register("fake", a.srv.URL, []string{"tg:"}, caps); err != nil {
		t.Fatalf("register: %v", err)
	}
	return a, reg
}

// await returns the first recorded send, or fails.
func (a *holdAdapter) await(t *testing.T) map[string]any {
	t.Helper()
	var first map[string]any
	awaitCond(t, "the adapter never received the notice", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		if len(a.sent) == 0 {
			return false
		}
		first = a.sent[0]
		return true
	})
	return first
}

// holdE2E wires a DB, a real chanreg-backed Deliverer and a Loop that share it,
// with `hold:mcp:like` blocking on folder atlas and `u` an operator.
func holdE2E(t *testing.T, caps map[string]bool) (*DB, *Server, *Loop, *holdAdapter) {
	t.Helper()
	db, loop, _ := recLoop(t)
	a, reg := newHoldAdapter(t, caps)
	deliver := NewChannelDeliverer(reg, nil, nil)
	loop.deliver = deliver
	srv := &Server{db: db, deliver: deliver}

	holdRule(t, db, "mcp:like", auth.EffectHoldBlock)
	if err := addMembership(db, "u", "role:operator", "test"); err != nil {
		t.Fatal(err)
	}
	return db, srv, loop, a
}

// TestHoldE2E_ButtonPressReleasesTheBlockedCall is the whole chain in one test,
// which is the only way to know it works: a blocking hold suspends the agent's
// call, the notice reaches the wire carrying its answers, the FIRST answer's
// data — used verbatim, exactly as an adapter delivers a press — resolves the
// hold through the ordinary command path, and the suspended call then runs.
//
// It deliberately reads the button data off the WIRE rather than rebuilding it:
// if the notice and the command parser ever disagree about the text, this fails.
func TestHoldE2E_ButtonPressReleasesTheBlockedCall(t *testing.T) {
	waitForVerdict(t)

	db, srv, loop, adapter := holdE2E(t, map[string]bool{"send_text": true, "buttons": true})
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t1"})

	held := make(chan bool, 1)
	go func() {
		_, h := gate("mcp:like", map[string]any{"platform_id": "m7"})
		held <- h
	}()

	notice := adapter.await(t)
	options, ok := notice["options"].([]any)
	if !ok || len(options) == 0 {
		t.Fatalf("the notice carried no answers: %#v", notice)
	}
	press, _ := options[0].(map[string]any)["data"].(string)
	if press == "" {
		t.Fatalf("the first answer carries no data: %#v", options[0])
	}

	// This is exactly what teled/discd/slakd deliver on a press: the data, as
	// text, from the presser.
	if !loop.steer("tg:1", core.Message{
		ChatJID: "tg:1", Sender: "u", Content: press, Timestamp: time.Now().UTC(),
	}, "atlas") {
		t.Fatalf("the button's data %q was not consumed as a command", press)
	}

	select {
	case h := <-held:
		if h {
			t.Fatal("the approved call must run inline, not stay held")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate never noticed the approval")
	}

	rows, err := db.ListPendingActions("atlas", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Status != PendingApproved {
		t.Fatalf("want one approved row, got %+v", rows)
	}
}

// TestHoldE2E_RejectPressKeepsTheCallUnrun — the second button. The call must not
// run, and the row records who said no.
func TestHoldE2E_RejectPressKeepsTheCallUnrun(t *testing.T) {
	waitForVerdict(t)

	db, srv, loop, adapter := holdE2E(t, map[string]bool{"send_text": true, "buttons": true})
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t1"})

	held := make(chan bool, 1)
	go func() {
		_, h := gate("mcp:like", map[string]any{"platform_id": "m7"})
		held <- h
	}()

	options := adapter.await(t)["options"].([]any)
	press := options[1].(map[string]any)["data"].(string)
	if !loop.steer("tg:1", core.Message{
		ChatJID: "tg:1", Sender: "u", Content: press, Timestamp: time.Now().UTC(),
	}, "atlas") {
		t.Fatalf("the reject button's data %q was not consumed", press)
	}

	select {
	case h := <-held:
		if !h {
			t.Fatal("a rejected call must never run")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate never noticed the rejection")
	}
	rows, _ := db.ListPendingActions("atlas", "")
	if rows[0].Status != PendingRejected || rows[0].ReviewedBy != "u" {
		t.Fatalf("want rejected by u, got %+v", rows[0])
	}
}

// TestHoldE2E_ChannelWithoutButtonsStillGetsAnAnswerablePrompt — the fold, end to
// end. An adapter with no button surface receives no options field at all, and
// the answers are in the text where a reader can type them. Typing one resolves
// the hold exactly as a press does.
func TestHoldE2E_ChannelWithoutButtonsStillGetsAnAnswerablePrompt(t *testing.T) {
	waitForVerdict(t)

	_, srv, loop, adapter := holdE2E(t, map[string]bool{"send_text": true})
	gate := srv.holdGate(turnMCP{folder: "atlas", chatJID: "tg:1", turnID: "t1"})

	held := make(chan bool, 1)
	go func() {
		_, h := gate("mcp:like", map[string]any{"platform_id": "m7"})
		held <- h
	}()

	notice := adapter.await(t)
	if _, present := notice["options"]; present {
		t.Error("a channel without the buttons cap must not receive options")
	}
	text, _ := notice["content"].(string)
	id := firstApproveID(t, text)

	if !loop.steer("tg:1", core.Message{
		ChatJID: "tg:1", Sender: "u", Content: "/approve " + id, Timestamp: time.Now().UTC(),
	}, "atlas") {
		t.Fatal("the typed command was not consumed")
	}
	select {
	case h := <-held:
		if h {
			t.Fatal("the approved call must run inline")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the gate never noticed the approval")
	}
}

// firstApproveID pulls the id out of the `/approve <id>` the folded notice
// offers, so the test types what a reader would actually read.
func firstApproveID(t *testing.T, text string) string {
	t.Helper()
	_, rest, ok := strings.Cut(text, "/approve ")
	if fields := strings.Fields(rest); ok && len(fields) > 0 {
		return fields[0]
	}
	t.Fatalf("the folded notice offers no /approve line:\n%s", text)
	return ""
}
