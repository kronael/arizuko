package chanlib

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

func TestChunk(t *testing.T) {
	tests := []struct {
		name string
		s    string
		max  int
		want []string
	}{
		{"empty", "", 10, []string{""}},
		{"shorter", "abc", 10, []string{"abc"}},
		{"exact", "abcde", 5, []string{"abcde"}},
		{"two_chunks", "abcdef", 3, []string{"abc", "def"}},
		{"three_chunks", "abcdefgh", 3, []string{"abc", "def", "gh"}},
		{"one_char", "abcdef", 1, []string{"a", "b", "c", "d", "e", "f"}},
		{"unicode_bytes", "aaaa\U0001F600", 5, []string{"aaaa", "\U0001F600"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Chunk(tt.s, tt.max)
			if len(got) != len(tt.want) {
				t.Fatalf("len = %d, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestEnvOr(t *testing.T) {
	k := "CHANLIB_TEST_ENVOR_KEY"

	os.Unsetenv(k)
	if got := EnvOr(k, "fallback"); got != "fallback" {
		t.Fatalf("unset: got %q, want fallback", got)
	}

	os.Setenv(k, "val")
	defer os.Unsetenv(k)
	if got := EnvOr(k, "fallback"); got != "val" {
		t.Fatalf("set: got %q, want val", got)
	}

	os.Setenv(k, "")
	if got := EnvOr(k, "fallback"); got != "fallback" {
		t.Fatalf("empty: got %q, want fallback", got)
	}
}

func TestAuthValidToken(t *testing.T) {
	var called bool
	h := Auth("s3cret", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	w := httptest.NewRecorder()
	h(w, req)

	if !called {
		t.Fatal("handler not called")
	}
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestAuthInvalidToken(t *testing.T) {
	var called bool
	h := Auth("s3cret", func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	w := httptest.NewRecorder()
	h(w, req)

	if called {
		t.Fatal("handler should not be called")
	}
	if w.Code != 401 {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestAuthEmptySecretPassesAll(t *testing.T) {
	var called bool
	h := Auth("", func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(200)
	})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if !called {
		t.Fatal("handler not called with empty secret")
	}
}

func TestAuthMissingHeader(t *testing.T) {
	var called bool
	h := Auth("s3cret", func(w http.ResponseWriter, r *http.Request) {
		called = true
	})

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	h(w, req)

	if called {
		t.Fatal("handler should not be called without header")
	}
	if w.Code != 401 {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	WriteJSON(w, map[string]string{"hello": "world"})

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["hello"] != "world" {
		t.Fatalf("body = %v", got)
	}
}

func TestWriteErr(t *testing.T) {
	w := httptest.NewRecorder()
	WriteErr(w, 403, "forbidden")

	if w.Code != 403 {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content-type = %q", ct)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got["ok"] != false {
		t.Fatalf("ok = %v, want false", got["ok"])
	}
	if got["error"] != "forbidden" {
		t.Fatalf("error = %v", got["error"])
	}
}

func TestPostSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			t.Errorf("method = %s", r.Method)
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type = %s", r.Header.Get("Content-Type"))
		}
		if r.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("auth = %s", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"value":42}`))
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "s")
	var out struct{ Value int }
	err := rc.Post("/test", map[string]string{"k": "v"}, "tok", &out)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	if out.Value != 42 {
		t.Fatalf("value = %d, want 42", out.Value)
	}
}

func TestPostNoAuth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("expected no auth header, got %q", r.Header.Get("Authorization"))
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "")
	var out struct{ OK bool }
	err := rc.Post("/test", nil, "", &out)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
}

func TestPostNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "s")
	var out struct{}
	err := rc.Post("/fail", nil, "tok", &out)
	if err == nil {
		t.Fatal("expected error for 500")
	}
	if !strings.Contains(err.Error(), "status 500") {
		t.Fatalf("err = %v", err)
	}
}

func TestPostConnectionError(t *testing.T) {
	rc := NewRouterClient("http://127.0.0.1:1", "s")
	var out struct{}
	err := rc.Post("/fail", nil, "tok", &out)
	if err == nil {
		t.Fatal("expected connection error")
	}
}

func TestRegisterSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/channels/register" {
			t.Errorf("path = %s", r.URL.Path)
		}
		w.Write([]byte(`{"ok":true,"token":"abc123"}`))
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "secret")
	tok, err := rc.Register("tg", "http://tg:9001", []string{"tg:"}, map[string]bool{"send_text": true})
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if tok != "abc123" {
		t.Fatalf("token = %q, want abc123", tok)
	}
}

func TestRegisterRejected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error":"name taken"}`))
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "s")
	_, err := rc.Register("tg", "http://tg:9001", nil, nil)
	if err == nil {
		t.Fatal("expected error for rejected register")
	}
	if !strings.Contains(err.Error(), "name taken") {
		t.Fatalf("err = %v", err)
	}
}

func TestSendMessageSuccessFirstTry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "s")
	rc.SetToken("tok")
	err := rc.SendMessage(InboundMsg{ID: "1", Content: "hi"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if c := calls.Load(); c != 1 {
		t.Fatalf("calls = %d, want 1", c)
	}
}

func TestSendMessageRetrySuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.Write([]byte(`{"ok":false,"error":"busy"}`))
			return
		}
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "s")
	rc.SetToken("tok")
	err := rc.SendMessage(InboundMsg{ID: "1", Content: "hi"})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if c := calls.Load(); c != 2 {
		t.Fatalf("calls = %d, want 2", c)
	}
}

func TestSendMessageBothFail(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"ok":false,"error":"down"}`))
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "s")
	rc.SetToken("tok")
	err := rc.SendMessage(InboundMsg{ID: "1"})
	if err == nil {
		t.Fatal("expected error when both attempts fail")
	}
	if !strings.Contains(err.Error(), "down") {
		t.Fatalf("err = %v", err)
	}
}

// On 401 from /v1/messages (router has auto-deregistered our channel),
// SendMessage must call Register again with the saved params, get a
// fresh token, and retry. Without this the adapter loops forever
// hitting 401 and platform messages back up indefinitely.
func TestSendMessage_401_TriggersReregisterAndRetries(t *testing.T) {
	var sendCalls, registerCalls atomic.Int32
	var firstSendIs401 atomic.Bool
	firstSendIs401.Store(true)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/channels/register":
			registerCalls.Add(1)
			w.Write([]byte(`{"ok":true,"token":"new-tok"}`))
		case "/v1/messages":
			sendCalls.Add(1)
			if firstSendIs401.Swap(false) {
				w.WriteHeader(http.StatusUnauthorized)
				w.Write([]byte(`{"ok":false,"error":"invalid token"}`))
				return
			}
			// On retry, succeed
			w.Write([]byte(`{"ok":true}`))
		}
	}))
	defer srv.Close()

	rc := NewRouterClient(srv.URL, "s")
	if _, err := rc.Register("tg", srv.URL, []string{"telegram:"}, nil); err != nil {
		t.Fatalf("register: %v", err)
	}
	if registerCalls.Load() != 1 {
		t.Fatalf("first register: calls = %d, want 1", registerCalls.Load())
	}

	if err := rc.SendMessage(InboundMsg{ID: "1", Content: "hi"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if sendCalls.Load() != 2 {
		t.Errorf("send calls = %d, want 2 (initial 401 + retry)", sendCalls.Load())
	}
	if registerCalls.Load() != 2 {
		t.Errorf("register calls = %d, want 2 (initial + recovery)", registerCalls.Load())
	}
}
