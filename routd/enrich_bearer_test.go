package routd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/chanreg"
	"github.com/kronael/arizuko/core"
)

func fileServer(t *testing.T) (*httptest.Server, func() string) {
	t.Helper()
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte("file-bytes"))
	}))
	t.Cleanup(srv.Close)
	return srv, func() string { return auth }
}

func enrichFrom(t *testing.T, adapters *chanreg.Registry, fileURL string) {
	t.Helper()
	bearer := func(context.Context) (string, error) { return "routd-token", nil }
	db, l, _ := enrichLoop(t, MediaConfig(true, 1<<20, "", "", false, false, bearer, adapters))
	raw, _ := json.Marshal([]chanlib.InboundAttachment{{URL: fileURL, Mime: "image/png", Filename: "a.png"}})
	msg := &core.Message{ID: "m1", ChatJID: "tg:1", Content: "x",
		Timestamp: time.Now().UTC(), Attachments: string(raw)}
	if err := db.PutMessage(*msg); err != nil {
		t.Fatal(err)
	}
	l.enrichAttachments(context.Background(), msg, "demo")
	if !strings.Contains(msg.Content, "<attachment path=") {
		t.Fatalf("download failed: %q", msg.Content)
	}
}

func TestEnrich_NonAdapterHostGetsNoBearer(t *testing.T) {
	adapter, _ := fileServer(t)
	cdn, cdnAuth := fileServer(t)
	reg := chanreg.New()
	if _, err := reg.Register("teled", adapter.URL, []string{"tg:"}, nil); err != nil {
		t.Fatal(err)
	}

	enrichFrom(t, reg, cdn.URL+"/img.png")

	if got := cdnAuth(); got != "" {
		t.Errorf("non-adapter host received Authorization %q", got)
	}
}

func TestEnrich_AdapterHostGetsBearer(t *testing.T) {
	adapter, adapterAuth := fileServer(t)
	reg := chanreg.New()
	if _, err := reg.Register("teled", adapter.URL, []string{"tg:"}, nil); err != nil {
		t.Fatal(err)
	}

	enrichFrom(t, reg, adapter.URL+"/files/abc")

	if got := adapterAuth(); got != "Bearer routd-token" {
		t.Errorf("adapter host Authorization = %q, want the service bearer", got)
	}
}
