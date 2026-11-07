package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kronael/arizuko/chanlib"
)

func TestSendReply(t *testing.T) {
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/drive/v3/files/file/comments/thread/replies" || r.URL.Query().Get("fields") != "id" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["content"] != "hello" {
			t.Errorf("body = %v, error = %v", body, err)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"id":"reply"}`))
	}))
	defer server.Close()
	b := &bot{cred: &credential{apiBase: server.URL, http: server.Client()}}
	req := chanlib.SendRequest{ChatJID: "gdrive:file/c/thread", Content: "hello"}
	id, err := b.Send(req)
	if err != nil || id != "gd-rfile.thread.reply" {
		t.Fatalf("id = %q, error = %v", id, err)
	}
	for _, tc := range []struct {
		status int
		want   error
	}{{429, chanlib.ErrNotDelivered}, {503, chanlib.ErrNotDelivered}, {403, chanlib.ErrPermanent}} {
		status = tc.status
		if _, err := b.Send(req); !errors.Is(err, tc.want) {
			t.Errorf("status %d: %v", status, err)
		}
	}
	req.ChatJID = "gdrive:file"
	if _, err := b.Send(req); !errors.Is(err, chanlib.ErrInvalidRequest) {
		t.Errorf("file JID: %v", err)
	}
}
