package chanreg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kronael/arizuko/chanlib"
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

func TestHTTPChannelEditCarriesAdapterError(t *testing.T) {
	for _, tt := range []struct {
		name      string
		status    int
		permanent bool
	}{
		{"bad request", http.StatusBadRequest, true},
		{"bad gateway", http.StatusBadGateway, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/edit" {
					t.Errorf("path = %q, want /edit", r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, err := w.Write([]byte(`{"error":"discord edit: Unknown Message"}`))
				if err != nil {
					t.Error(err)
				}
			}))
			defer srv.Close()
			ch := NewHTTPChannel(&Entry{
				Name: "discord", URL: srv.URL,
				Capabilities: map[string]bool{"edit": true},
			}, nil)

			err := ch.Edit(context.Background(), "discord:g/c", "m1", "done", "")
			if err == nil {
				t.Fatal("edit must return the adapter error")
			}
			if got := errors.Is(err, ErrPermanent); got != tt.permanent {
				t.Errorf("permanent = %v, want %v: %v", got, tt.permanent, err)
			}
			if !strings.Contains(err.Error(), "discord edit: Unknown Message") {
				t.Errorf("adapter reason missing: %v", err)
			}
		})
	}
}

func TestHTTPChannelEditUnsupportedHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, err := w.Write([]byte(`{"tool":"edit","platform":"discord","hint":"editing disabled"}`))
		if err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	ch := NewHTTPChannel(&Entry{
		Name: "discord", URL: srv.URL,
		Capabilities: map[string]bool{"edit": true},
	}, nil)

	err := ch.Edit(context.Background(), "discord:g/c", "m1", "done", "")
	var unsupported *chanlib.UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("want structured unsupported error, got %v", err)
	}
	if !errors.Is(err, ErrUnsupported) || errors.Is(err, ErrPermanent) {
		t.Errorf("501 classification changed: %v", err)
	}
	if unsupported.Tool != "edit" || unsupported.Platform != "discord" || unsupported.Hint != "editing disabled" {
		t.Errorf("unsupported detail = %+v", unsupported)
	}
}
