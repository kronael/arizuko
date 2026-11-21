package transport

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestHTTPUpstreamRejectsFailureStatus(t *testing.T) {
	for _, status := range []int{200, 202, 400, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
			}))
			defer s.Close()
			_, err := New(s.URL, nil).Call(json.RawMessage(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
			if status < 300 && err != nil {
				t.Fatal(err)
			}
			if status >= 300 && (err == nil || !strings.Contains(err.Error(), strconv.Itoa(status))) {
				t.Fatalf("status %d: error = %v", status, err)
			}
		})
	}
}
