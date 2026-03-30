package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/onvos/arizuko/chanlib"
	"github.com/onvos/arizuko/chanreg"
	"github.com/onvos/arizuko/store"
)

func setup(t *testing.T) (*Server, *chanreg.Registry, *store.Store) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "store"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })

	reg := chanreg.New("test-secret")
	srv := New(reg, s)
	return srv, reg, s
}

func postJSON(handler http.Handler, path string, body any, token string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", path, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func getJSON(handler http.Handler, path, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func decodeResp(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(w.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRegisterChannel(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name:        "telegram",
		URL:         "http://tg:9001",
		JIDPrefixes: []string{"tg:"},
		Capabilities: map[string]bool{
			"send_text": true,
			"typing":    true,
		},
	}, "test-secret")

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	resp := decodeResp(t, w)
	if resp["ok"] != true {
		t.Errorf("ok = %v", resp["ok"])
	}
	if resp["token"] == nil || resp["token"] == "" {
		t.Error("expected token in response")
	}
}

func TestRegisterBadSecret(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name: "telegram", URL: "http://tg:9001", JIDPrefixes: []string{"tg:"},
	}, "wrong-secret")

	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRegisterMissingFields(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name: "telegram",
	}, "test-secret")

	if w.Code != 400 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestDeliverMessage(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID:    "tg:123",
		Sender:     "tg:456",
		SenderName: "Alice",
		Content:    "hello world",
		Timestamp:  1709942400,
	}, token)

	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	msgs, _, err := s.NewMessages([]string{"tg:123"}, time.Time{}, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Content != "hello world" {
		t.Errorf("content = %q", msgs[0].Content)
	}
	if msgs[0].Name != "Alice" {
		t.Errorf("sender_name = %q", msgs[0].Name)
	}
}

func TestDeliverMessageBadToken(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:123", Content: "hello",
	}, "invalid-token")

	if w.Code != 401 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestDeliverMessageMissingFields(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/messages", messageReq{
		ChatJID: "tg:123",
		// missing content
	}, token)

	if w.Code != 400 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestChatMetadata(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/chats", chatReq{
		ChatJID: "tg:123",
		Name:    "Dev Chat",
		IsGroup: true,
	}, token)

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestDeregister(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	var deregistered string
	srv.OnDeregister(func(name string) { deregistered = name })

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	w := postJSON(h, "/v1/channels/deregister", nil, token)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	if deregistered != "tg" {
		t.Errorf("deregistered = %q", deregistered)
	}
	if reg.Get("tg") != nil {
		t.Error("expected channel removed from registry")
	}
}

func TestListChannels(t *testing.T) {
	srv, reg, _ := setup(t)
	h := srv.Handler()

	reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)
	reg.Register("dc", "http://dc:9002", []string{"dc:"}, nil)

	w := getJSON(h, "/v1/channels", "test-secret")
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}

	resp := decodeResp(t, w)
	channels := resp["channels"].([]any)
	if len(channels) != 2 {
		t.Errorf("channels = %d, want 2", len(channels))
	}
}

func TestHealth(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	w := getJSON(h, "/health", "")
	if w.Code != 200 {
		t.Errorf("status = %d", w.Code)
	}
}

func TestRegisterCallbackCreatesChannel(t *testing.T) {
	srv, _, _ := setup(t)
	h := srv.Handler()

	var registeredName string
	srv.OnRegister(func(name string, ch *chanreg.HTTPChannel) {
		registeredName = name
	})

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name:        "telegram",
		URL:         "http://tg:9001",
		JIDPrefixes: []string{"tg:"},
	}, "test-secret")

	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	if registeredName != "telegram" {
		t.Errorf("registered = %q", registeredName)
	}
}

func TestDeliverMessageWithAttachments(t *testing.T) {
	srv, reg, s := setup(t)
	h := srv.Handler()

	token, _ := reg.Register("tg", "http://tg:9001", []string{"tg:"}, nil)

	body := messageReq{
		ChatJID:    "tg:123",
		Sender:     "tg:456",
		SenderName: "Alice",
		Content:    "[Photo]",
		Timestamp:  1709942400,
		Attachments: []chanlib.InboundAttachment{
			{Mime: "image/jpeg", Filename: "photo.jpg", URL: "http://teled:9001/files/abc123", Size: 2048},
		},
	}
	w := postJSON(h, "/v1/messages", body, token)
	if w.Code != 200 {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}

	msgs, _, err := s.NewMessages([]string{"tg:123"}, time.Time{}, "bot")
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("messages = %d, want 1", len(msgs))
	}
	if msgs[0].Attachments == "" {
		t.Error("attachments should be stored, got empty")
	}
	if !strings.Contains(msgs[0].Attachments, "image/jpeg") {
		t.Errorf("attachments JSON missing mime type, got %q", msgs[0].Attachments)
	}
	if !strings.Contains(msgs[0].Attachments, "teled:9001") {
		t.Errorf("attachments JSON missing URL, got %q", msgs[0].Attachments)
	}
}

func TestNoSecretAllowsAll(t *testing.T) {
	dir := t.TempDir()
	s, _ := store.Open(filepath.Join(dir, "store"))
	defer s.Close()

	reg := chanreg.New("") // no secret
	srv := New(reg, s)
	h := srv.Handler()

	w := postJSON(h, "/v1/channels/register", registerReq{
		Name:        "tg",
		URL:         "http://tg:9001",
		JIDPrefixes: []string{"tg:"},
	}, "")

	if w.Code != 200 {
		t.Errorf("status = %d with no secret", w.Code)
	}
}
