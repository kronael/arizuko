package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// A reply inside a thread that quotes a message Discord cannot resolve there
// (a parent-channel message, a deleted one) still lands IN the thread: the
// reference is sent with fail_if_not_exists=false, so Discord posts a plain
// message instead of refusing with "Unknown message". On sloth every reply
// quoting a parent-channel message from a thread was refused and the sweep's
// retry then landed it in the parent channel.
func TestSend_ReplyReferenceNeverFailsTheThreadPost(t *testing.T) {
	rec := &recorded{}
	srv := newRecordingDiscordServer(t, rec)
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	if _, err := b.Send(chanlib.SendRequest{
		ChatJID: "discord:g1/ch-1", Content: "in the thread",
		ReplyTo: "parent-msg-1", ThreadID: "th-7",
	}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	assertPath(t, rec, "POST", "/channels/th-7/messages")
	want := `"message_reference":{"message_id":"parent-msg-1","fail_if_not_exists":false}`
	for _, r := range rec.snapshot() {
		if r.Path == "/channels/th-7/messages" && !strings.Contains(r.Body, want) {
			t.Errorf("reference must not fail the post: body %s\nwant %s", r.Body, want)
		}
	}
}

// Discord's own 4xx reaches the agent as an ErrInvalidRequest carrying
// Discord's reason, not as a bare transport error routd retries ten times.
func TestSend_DiscordRefusalIsInvalidRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"code":50035,"message":"Invalid Form Body"}`))
	}))
	defer srv.Close()
	defer mockAllDiscordEndpoints(srv.URL)()

	b := &bot{session: newTestSession(t)}
	_, err := b.Send(chanlib.SendRequest{ChatJID: "discord:g1/ch-1", Content: "x", ThreadID: "th-7"})
	if !errors.Is(err, chanlib.ErrInvalidRequest) {
		t.Fatalf("err = %v, want ErrInvalidRequest", err)
	}
	if !strings.Contains(err.Error(), "Invalid Form Body") {
		t.Fatalf("err = %v, want Discord's reason in the text", err)
	}
}
