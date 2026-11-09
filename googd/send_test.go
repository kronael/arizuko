package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
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

func TestEditReply(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPatch || r.URL.Path != "/drive/v3/files/file/comments/thread/replies/reply" || r.URL.Query().Get("fields") != "id" {
			t.Errorf("request = %s %s", r.Method, r.URL.String())
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["content"] != "Retry 1/3 is running." {
			t.Errorf("body = %v, error = %v", body, err)
		}
		_, _ = w.Write([]byte(`{"id":"reply"}`))
	}))
	defer server.Close()
	b := &bot{cred: &credential{apiBase: server.URL, http: server.Client()}}
	if err := b.Edit(chanlib.EditRequest{ChatJID: "gdrive:file/c/thread", TargetID: "gd-rfile.thread.reply", Content: "Retry 1/3 is running."}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("edit requests = %d", calls)
	}
}

func TestDeleteReply(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodDelete || r.URL.Path != "/drive/v3/files/file/comments/thread/replies/reply" || r.URL.RawQuery != "" || r.ContentLength != 0 {
			t.Errorf("request = %s %s length=%d", r.Method, r.URL.String(), r.ContentLength)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	b := &bot{cred: &credential{apiBase: server.URL, http: server.Client()}}
	if err := b.Delete(chanlib.DeleteRequest{ChatJID: "gdrive:file/c/thread", TargetID: "gd-rfile.thread.reply"}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("delete requests = %d", calls)
	}
}

func TestReplyOperationRefusals(t *testing.T) {
	for _, tc := range []struct {
		status       int
		edit, delete error
	}{
		{403, chanlib.ErrPermanent, chanlib.ErrPermanent},
		{404, chanlib.ErrPermanent, nil},
		{429, chanlib.ErrNotDelivered, chanlib.ErrNotDelivered},
		{503, chanlib.ErrNotDelivered, chanlib.ErrNotDelivered},
	} {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Method == http.MethodPost {
				t.Error("refused operation posted again")
			}
			w.WriteHeader(tc.status)
		}))
		b := &bot{cred: &credential{apiBase: server.URL, http: server.Client()}}
		if err := b.Edit(chanlib.EditRequest{ChatJID: "gdrive:file/c/thread", TargetID: "gd-rfile.thread.reply", Content: "retry"}); !errors.Is(err, tc.edit) {
			t.Errorf("edit status %d: %v", tc.status, err)
		}
		if err := b.Delete(chanlib.DeleteRequest{ChatJID: "gdrive:file/c/thread", TargetID: "gd-rfile.thread.reply"}); !errors.Is(err, tc.delete) {
			t.Errorf("delete status %d: %v", tc.status, err)
		}
		server.Close()
		if calls != 2 {
			t.Fatalf("refusal requests = %d", calls)
		}
	}
}

func TestReplyOperationInvalidTarget(t *testing.T) {
	b := &bot{cred: &credential{}}
	for _, tc := range []struct{ jid, id string }{
		{"gdrive:file", "gd-rfile.thread.reply"},
		{"gdrive:file/c/thread", "gd-cfile.thread"},
		{"gdrive:file/c/thread", "gd-rother.thread.reply"},
		{"gdrive:file/c/thread", "gd-rfile.other.reply"},
		{"gdrive:file/c/thread", "gd-rfile.thread."},
		{"gdrive:file/c/thread", "gd-rfile.thread..."},
		{"gdrive:file/c/thread", "gd-rfile.thread.reply/other"},
	} {
		if err := b.Edit(chanlib.EditRequest{ChatJID: tc.jid, TargetID: tc.id, Content: "retry"}); !errors.Is(err, chanlib.ErrInvalidRequest) {
			t.Errorf("edit %+v: %v", tc, err)
		}
		if err := b.Delete(chanlib.DeleteRequest{ChatJID: tc.jid, TargetID: tc.id}); !errors.Is(err, chanlib.ErrInvalidRequest) {
			t.Errorf("delete %+v: %v", tc, err)
		}
	}
	if err := b.Edit(chanlib.EditRequest{ChatJID: "gdrive:file/c/thread", TargetID: "gd-rfile.thread.reply", Content: "retry"}); !errors.Is(err, chanlib.ErrNotDelivered) {
		t.Errorf("unavailable credential edit: %v", err)
	}
	if err := b.Delete(chanlib.DeleteRequest{ChatJID: "gdrive:file/c/thread", TargetID: "gd-rfile.thread.reply"}); !errors.Is(err, chanlib.ErrNotDelivered) {
		t.Errorf("unavailable credential delete: %v", err)
	}
}

func TestTurnStatusReplyLifecycle(t *testing.T) {
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodDelete {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, _ = w.Write([]byte(`{"id":"reply"}`))
	}))
	defer server.Close()
	b := &bot{cred: &credential{apiBase: server.URL, http: server.Client()}}
	jid := "gdrive:file/c/thread"
	id, err := b.Send(chanlib.SendRequest{ChatJID: jid, Content: "Server storage is full."})
	if err != nil {
		t.Fatal(err)
	}
	if err := b.Edit(chanlib.EditRequest{ChatJID: jid, TargetID: id, Content: "Retry 1/3 is running."}); err != nil {
		t.Fatal(err)
	}
	if err := b.Delete(chanlib.DeleteRequest{ChatJID: jid, TargetID: id}); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"POST /drive/v3/files/file/comments/thread/replies",
		"PATCH /drive/v3/files/file/comments/thread/replies/reply",
		"DELETE /drive/v3/files/file/comments/thread/replies/reply",
	}
	if !slices.Equal(methods, want) {
		t.Fatalf("lifecycle requests = %v, want %v", methods, want)
	}
}
