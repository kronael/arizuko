package chanreg

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// optionsEcho captures the /send body one call produced.
func optionsEcho(t *testing.T, caps map[string]bool, options []chanlib.Option) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer srv.Close()

	e := &Entry{Name: "tg", URL: srv.URL, JIDPrefixes: []string{"tg:"}, Capabilities: caps}
	ch := NewHTTPChannel(e, staticBearer("secret"))
	if _, err := ch.SendOptionsCtx(context.Background(), "tg:1", "pick one", options, "", "", "", ""); err != nil {
		t.Fatalf("SendOptionsCtx: %v", err)
	}
	return got
}

// TestSendOptions_NativeWhenAdapterHasButtons — an adapter that advertises
// `buttons` receives the options untouched and draws them itself.
func TestSendOptions_NativeWhenAdapterHasButtons(t *testing.T) {
	got := optionsEcho(t, map[string]bool{"send_text": true, "buttons": true},
		[]chanlib.Option{{Label: "Approve", Data: "/approve ab12"}})

	if got["content"] != "pick one" {
		t.Errorf("content was rewritten: %q", got["content"])
	}
	opts, ok := got["options"].([]any)
	if !ok || len(opts) != 1 {
		t.Fatalf("options not passed through: %#v", got["options"])
	}
	if o := opts[0].(map[string]any); o["data"] != "/approve ab12" {
		t.Errorf("data = %v, want the command the press replaces", o["data"])
	}
}

// TestSendOptions_FoldsForAChannelWithoutButtons is the one fold. A channel with
// no button surface must still deliver an ANSWERABLE prompt: the answers become
// text the reader can type, and the adapter never sees an options field it would
// ignore.
func TestSendOptions_FoldsForAChannelWithoutButtons(t *testing.T) {
	got := optionsEcho(t, map[string]bool{"send_text": true},
		[]chanlib.Option{{Label: "Approve", Data: "/approve ab12"}, {Label: "Reject", Data: "/reject ab12"}})

	if _, present := got["options"]; present {
		t.Error("an adapter without the buttons cap must not receive options")
	}
	content, _ := got["content"].(string)
	for _, want := range []string{"/approve ab12", "/reject ab12"} {
		if !strings.Contains(content, want) {
			t.Errorf("folded text must carry %q, got %q", want, content)
		}
	}
}

// TestSendOptions_LabelIsTheDataWhenUnset — Data defaults to the label, so a
// caller offering plain answers gets a button whose press sends that answer.
func TestSendOptions_LabelIsTheDataWhenUnset(t *testing.T) {
	if got := (chanlib.Option{Label: "prod"}).Send(); got != "prod" {
		t.Errorf("Send() = %q, want the label", got)
	}
}
