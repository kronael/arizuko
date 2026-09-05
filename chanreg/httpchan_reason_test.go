package chanreg

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A refused send carries the adapter's reason to the caller, on top of the
// permanent/transient class. The class alone left routd and the agent with a
// bare "status 502" and nothing to diagnose.
func TestHTTPChannelSendCarriesTheAdapterReason(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"ok":false,"error":"invalid request: discord send: HTTP 400 Bad Request, {\"code\":50035}"}`))
	}))
	defer srv.Close()
	e := &Entry{Name: "discord", URL: srv.URL, JIDPrefixes: []string{"discord:"},
		Capabilities: map[string]bool{"send_text": true}}
	ch := NewHTTPChannel(e, staticBearer("secret"))

	_, err := ch.Send("discord:g/c", "x", "", "", "", "")
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("err = %v, want ErrPermanent", err)
	}
	if !strings.Contains(err.Error(), "50035") {
		t.Fatalf("err = %v, want the adapter's reason in the text", err)
	}
}
