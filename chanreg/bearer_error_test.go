package chanreg

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestBearerErrorStopsRequests(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()
	channel := NewHTTPChannel(&Entry{Name: "test", URL: upstream.URL, Capabilities: map[string]bool{"send_text": true, "send_file": true, "fetch_history": true}}, func(context.Context) (string, error) { return "", context.DeadlineExceeded })
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("data"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, sendErr := channel.SendOptionsCtx(context.Background(), "test:chat", "hello", nil, "", "", "", "", "")
	_, fileErr := channel.SendFileCtx(context.Background(), "test:chat", file, "file", "", "", "", "", "", "")
	_, historyErr := channel.FetchHistory(context.Background(), "test:chat", time.Time{}, 1)
	for _, err := range []error{sendErr, fileErr, historyErr} {
		if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrPermanent) {
			t.Errorf("error = %v", err)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Errorf("adapter requests = %d", got)
	}
}
