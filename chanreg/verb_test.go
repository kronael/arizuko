package chanreg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

// Verbs gated on a capability must short-circuit to Unsupported when the
// adapter doesn't advertise the cap — never hit the network. A regression
// (dropping the HasCap guard) would surface as a confusing adapter-side 404
// instead of a clean "platform doesn't support this" hint to the agent.
func TestHTTPChannelVerbNoCapUnsupported(t *testing.T) {
	e := &Entry{Name: "x", URL: "http://should-not-be-called", JIDPrefixes: []string{"x:"},
		Capabilities: map[string]bool{}}
	ch := NewHTTPChannel(e, StaticBearer("secret"))
	ctx := context.Background()

	checks := map[string]error{
		"delete":  ch.Delete(ctx, "x:1", "m1"),
		"dislike": ch.Dislike(ctx, "x:1", "m1"),
		"edit":    ch.Edit(ctx, "x:1", "m1", "txt"),
		"pin":     ch.Pin(ctx, "x:1", "m1"),
		"unpin":   ch.Unpin(ctx, "x:1", "m1", false),
	}
	for verb, err := range checks {
		if !errors.Is(err, chanlib.ErrUnsupported) {
			t.Errorf("%s without cap = %v, want Unsupported", verb, err)
		}
	}

	if _, err := ch.Forward(ctx, "s|1", "x:2", ""); !errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("forward without cap = %v, want Unsupported", err)
	}
	if _, err := ch.Quote(ctx, "x:1", "s1", ""); !errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("quote without cap = %v, want Unsupported", err)
	}
	if _, err := ch.Repost(ctx, "x:1", "s1"); !errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("repost without cap = %v, want Unsupported", err)
	}
}

// A social verb whose target is gone (e.g. like/delete on a deleted message)
// makes the adapter return a non-200, non-501 status. postVerb must surface
// that as an error so the agent learns the action failed — not swallow it.
func TestHTTPChannelVerbNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest) // adapter: "message not found"
	}))
	defer srv.Close()

	e := &Entry{Name: "x", URL: srv.URL, JIDPrefixes: []string{"x:"},
		Capabilities: map[string]bool{"delete": true}}
	ch := NewHTTPChannel(e, StaticBearer("secret"))

	err := ch.Delete(context.Background(), "x:1", "missing")
	if err == nil {
		t.Fatal("delete of missing target must error")
	}
	if errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("400 must not be Unsupported (that's reserved for 501): %v", err)
	}
}

// 501 from a gated verb decodes to Unsupported even though the cap was
// advertised — the adapter knows best at request time.
func TestHTTPChannelVerb501IsUnsupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
	}))
	defer srv.Close()

	e := &Entry{Name: "x", URL: srv.URL, JIDPrefixes: []string{"x:"},
		Capabilities: map[string]bool{"delete": true}}
	ch := NewHTTPChannel(e, StaticBearer("secret"))

	if err := ch.Delete(context.Background(), "x:1", "m1"); !errors.Is(err, chanlib.ErrUnsupported) {
		t.Errorf("501 must map to Unsupported, got %v", err)
	}
}

// Like is NOT cap-gated (every platform with reactions implements it), so an
// emoji-reaction on a missing message reaches the adapter; a non-200 surfaces.
func TestHTTPChannelLikeMissingTarget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	e := &Entry{Name: "x", URL: srv.URL, JIDPrefixes: []string{"x:"}}
	ch := NewHTTPChannel(e, StaticBearer("secret"))

	if err := ch.Like(context.Background(), "x:1", "gone", "👍"); err == nil {
		t.Fatal("like on missing target must error")
	}
}
