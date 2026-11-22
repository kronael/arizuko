package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamResponseLimit(t *testing.T) {
	for _, size := range []int{8 << 20, (8 << 20) + 1} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(strings.Repeat("x", size)))
		}))
		body, err := New(upstream.URL, nil).Call(json.RawMessage(`{}`))
		upstream.Close()
		if size == 8<<20 && (err != nil || len(body) != size) {
			t.Errorf("size %d: bytes=%d err=%v", size, len(body), err)
		}
		if size > 8<<20 && (err == nil || body != nil) {
			t.Errorf("size %d: bytes=%d err=%v", size, len(body), err)
		}
	}
}
