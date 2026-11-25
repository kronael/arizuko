package routd

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/retry"
	"github.com/kronael/arizuko/store"
)

func TestMalformedAttachmentIsVisible(t *testing.T) {
	db, loop, _ := enrichLoop(t, mediaConfig{Enabled: true})
	msg := &core.Message{ID: "bad", ChatJID: "tg:bad", Content: "file", Timestamp: time.Now().UTC(), Attachments: "{"}
	if err := db.PutMessage(*msg); err != nil {
		t.Fatal(err)
	}
	if err := loop.enrichAttachments(context.Background(), msg, "demo", false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg.Content, "unavailable=") {
		t.Fatalf("malformed attachment was silent: %q", msg.Content)
	}
}

func TestAttachmentRetryAfterAndInlineCleanup(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Retry-After", "600")
		http.Error(w, "busy", http.StatusTooManyRequests)
	}))
	defer server.Close()
	db, loop, groups := enrichLoop(t, mediaConfig{Enabled: true, MaxBytes: 1024})
	raw, err := json.Marshal([]chanlib.InboundAttachment{{Data: base64.StdEncoding.EncodeToString([]byte("inline")), Filename: "inline.txt", Mime: "text/plain"}, {URL: server.URL, Filename: "remote.txt", Mime: "text/plain"}})
	if err != nil {
		t.Fatal(err)
	}
	msg := &core.Message{ID: "retry", ChatJID: "chat", Content: "original", Timestamp: time.Now().UTC(), Attachments: string(raw)}
	if err := db.PutMessage(*msg); err != nil {
		t.Fatal(err)
	}
	before := time.Now()
	if err := loop.enrichAttachments(context.Background(), msg, "demo", true); !errors.Is(err, errFetchDeferred) {
		t.Fatalf("defer: %v", err)
	}
	if requests != 1 || msg.Content != "original" || msg.Attachments != string(raw) {
		t.Fatalf("attempt: requests=%d msg=%+v", requests, msg)
	}
	files, err := filepath.Glob(filepath.Join(groups, "demo", "media", "*", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("files: %v %v", files, err)
	}
	work, found, err := db.store().ReadAttachmentRetry(context.Background(), *msg)
	if err != nil || !found || work.Failures != 1 || work.NextAt.Before(before.Add(600*time.Second)) {
		t.Fatalf("retry: %+v %v", work, err)
	}
	if err := loop.enrichAttachments(context.Background(), msg, "demo", true); !errors.Is(err, errFetchDeferred) || requests != 1 {
		t.Fatalf("early retry: %d %v", requests, err)
	}
}

func TestSavedTurnAttachmentPostponesWithoutSpendingRetry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusBadGateway) }))
	defer server.Close()
	db, srv, runner, _ := attachmentRoutd(t)
	now := time.Now().UTC()
	raw, err := json.Marshal([]chanlib.InboundAttachment{{URL: server.URL, Filename: "file.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	msg := core.Message{ID: "saved", ChatJID: "telegram:group/1", Sender: "user", Content: "input", Timestamp: now, Attachments: string(raw)}
	if err := db.PutMessage(msg); err != nil {
		t.Fatal(err)
	}
	keys, err := turnMessageKeys([]core.Message{msg})
	if err != nil {
		t.Fatal(err)
	}
	tc := store.TurnContext{TurnID: msg.ID, Folder: "demo", ChatJID: msg.ChatJID, Trigger: msg.Sender, InputLast: store.MessageCursor(msg), InputKeys: keys, NextAttemptAt: now}
	if ok, err := db.store().SavePendingTurn(context.Background(), tc, ""); err != nil || !ok {
		t.Fatalf("save: %v %v", ok, err)
	}
	before, _ := db.GetTurnContext(tc.TurnID)
	if _, err := srv.loop.replay(tc.TurnID); err != nil {
		t.Fatal(err)
	}
	after, _ := db.GetTurnContext(tc.TurnID)
	if after.State != store.TurnPendingRetry || after.RetryCount != before.RetryCount || !after.NextAttemptAt.After(before.NextAttemptAt) || runner.gotTurn != "" {
		t.Fatalf("saved turn: before=%+v after=%+v ran=%q", before, after, runner.gotTurn)
	}
}

func TestAttachmentTenthFailureIsTerminalOnce(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests++; http.Error(w, "busy", http.StatusBadGateway) }))
	defer server.Close()
	db, loop, _ := enrichLoop(t, mediaConfig{Enabled: true, MaxBytes: 1024})
	raw, err := json.Marshal([]chanlib.InboundAttachment{{URL: server.URL, Filename: "remote.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	msg := &core.Message{ID: "terminal", ChatJID: "chat", Content: "original", Timestamp: time.Now().UTC(), Attachments: string(raw)}
	if err := db.PutMessage(*msg); err != nil {
		t.Fatal(err)
	}
	snap, _, err := db.store().ReadAttachmentRetry(context.Background(), *msg)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := db.store().DeferAttachments(context.Background(), snap, retry.Decision{Failures: 9, NextAt: time.Now().Add(-time.Second)}, ""); err != nil || !ok {
		t.Fatalf("seed: %v %v", ok, err)
	}
	if err := loop.enrichAttachments(context.Background(), msg, "demo", true); err != nil {
		t.Fatal(err)
	}
	if err := loop.enrichAttachments(context.Background(), msg, "demo", true); err != nil {
		t.Fatal(err)
	}
	if requests != 1 || strings.Count(msg.Content, "unavailable=") != 1 {
		t.Fatalf("terminal: requests=%d content=%q", requests, msg.Content)
	}
	work, _, err := db.store().ReadAttachmentRetry(context.Background(), *msg)
	if err != nil || work.State != "failed" || work.Failures != 10 || work.Message.Attachments != string(raw) {
		t.Fatalf("state: %+v %v", work, err)
	}
}

func TestAttachmentFailedCommitKeepsCallerAndSearch(t *testing.T) {
	db, loop, groups := enrichLoop(t, mediaConfig{Enabled: true, MaxBytes: 1024})
	msg := &core.Message{ID: "changed", ChatJID: "tg:chat", Content: "original", Timestamp: time.Now().UTC()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := db.store().SetMessageContent(*msg, "edited"); err != nil {
			t.Error(err)
		}
		_, _ = w.Write([]byte("file"))
	}))
	defer server.Close()
	raw, err := json.Marshal([]chanlib.InboundAttachment{{URL: server.URL, Filename: "uniqueword.txt", Mime: "text/plain"}})
	if err != nil {
		t.Fatal(err)
	}
	msg.Attachments = string(raw)
	if err := db.PutMessage(*msg); err != nil {
		t.Fatal(err)
	}
	if err := loop.enrichAttachments(context.Background(), msg, "demo", true); err == nil {
		t.Fatal("stale enrichment committed")
	}
	if msg.Content != "original" || msg.Attachments != string(raw) {
		t.Fatalf("caller changed: %+v", msg)
	}
	files, err := filepath.Glob(filepath.Join(groups, "demo", "media", "*", "*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("files: %v %v", files, err)
	}
	if hits := searched(t, db, "edited", "tg:chat", "", "", 10); len(hits) != 1 {
		t.Fatalf("the edit that won is not searchable: %+v", hits)
	}
	if hits := searched(t, db, "uniqueword", "tg:chat", "", "", 10); len(hits) != 0 {
		t.Fatalf("uncommitted search result: %+v", hits)
	}
}

func TestAttachmentCutBodyRemovesPartialFile(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte("partial"))
	}))
	defer server.Close()
	dest := filepath.Join(t.TempDir(), "partial")
	if err := downloadFile(context.Background(), server.URL, dest, nil, 1024); err == nil {
		t.Fatal("cut body succeeded")
	}
	if requests != 1 {
		t.Fatalf("requests=%d", requests)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial file: %v", err)
	}
}

func TestLegacyAttachmentStartupDiscovery(t *testing.T) {
	for _, saved := range []bool{false, true} {
		name := "unread"
		if saved {
			name = "saved"
		}
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "busy", http.StatusBadGateway) }))
			defer server.Close()
			db, srv, _, _ := attachmentRoutd(t)
			now := time.Now().UTC()
			raw, err := json.Marshal([]chanlib.InboundAttachment{{URL: server.URL, Filename: "legacy.txt"}})
			if err != nil {
				t.Fatal(err)
			}
			msg := core.Message{ID: "legacy", ChatJID: "telegram:group/1", Sender: "user", Content: "legacy", Timestamp: now, Attachments: string(raw)}
			if err := db.PutMessage(msg); err != nil {
				t.Fatal(err)
			}
			if saved {
				keys, err := turnMessageKeys([]core.Message{msg})
				if err != nil {
					t.Fatal(err)
				}
				tc := store.TurnContext{TurnID: msg.ID, Folder: "demo", ChatJID: msg.ChatJID, Trigger: msg.Sender, InputLast: store.MessageCursor(msg), InputKeys: keys, NextAttemptAt: now.Add(-time.Second)}
				if ok, err := db.store().SavePendingTurn(context.Background(), tc, ""); err != nil || !ok {
					t.Fatalf("save: %v %v", ok, err)
				}
			}
			done := make(chan error, 1)
			srv.loop.q.mu.Lock()
			srv.loop.q.shuttingDown = false
			srv.loop.q.mu.Unlock()
			srv.loop.q.SetProcessMessagesFn(func(key string) (bool, error) {
				had, err := srv.loop.processGroupMessages(key)
				done <- err
				return had, err
			})
			t.Cleanup(srv.loop.StopQueue)
			srv.loop.recoverPending()
			srv.loop.sweepChats(context.Background())
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("startup did not dispatch legacy input")
			}
			work, found, err := db.store().ReadAttachmentRetry(context.Background(), msg)
			if err != nil || !found || work.State != "pending" || work.Failures != 1 || work.NextAt.IsZero() {
				t.Fatalf("legacy retry: %+v %v", work, err)
			}
		})
	}
}

func TestAttachmentLongExtension(t *testing.T) {
	name := sanitizeFilename("a." + strings.Repeat("z", 201))
	if len(name) != 200 {
		t.Fatalf("filename length = %d, want 200", len(name))
	}
}

func TestAttachmentGeneratedNameStaysInMedia(t *testing.T) {
	for _, filename := range []string{"", "exists.bin"} {
		t.Run(filename, func(t *testing.T) {
			db, loop, groups := enrichLoop(t, mediaConfig{Enabled: true, MaxBytes: 1024})
			msg := core.Message{
				ID: "../../victim", ChatJID: "web:demo", Sender: "u", Content: "file",
				Timestamp: time.Now(),
				Attachments: `[{"data":"b3ZlcndyaXR0ZW4=","filename":"` + filename +
					`","mime":"application/octet-stream"}]`,
			}
			victim := filepath.Join(groups, "demo", "victim-0.bin")
			if err := os.WriteFile(victim, []byte("original"), 0o600); err != nil {
				t.Fatal(err)
			}
			if filename != "" {
				dir := filepath.Join(groups, "demo", "media", time.Now().Format("20060102"))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(dir, filename), []byte("existing"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := db.PutMessage(msg); err != nil {
				t.Fatal(err)
			}
			if err := loop.enrichAttachments(context.Background(), &msg, "demo", true); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(victim)
			if err != nil || string(data) != "original" {
				t.Fatalf("victim = %q, error = %v", data, err)
			}
		})
	}
}

func TestAttachmentInlineLimit(t *testing.T) {
	for _, size := range []int{4, 5} {
		t.Run(string(rune('0'+size)), func(t *testing.T) {
			db, loop, groups := enrichLoop(t, mediaConfig{Enabled: true, MaxBytes: 4})
			payload := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", size)))
			msg := core.Message{
				ID: "inline", ChatJID: "web:demo", Content: "file", Timestamp: time.Now(),
				Attachments: `[{"data":"` + payload + `","filename":"inline.bin"}]`,
			}
			if err := db.PutMessage(msg); err != nil {
				t.Fatal(err)
			}
			if err := loop.enrichAttachments(context.Background(), &msg, "demo", true); err != nil {
				t.Fatal(err)
			}
			files, err := filepath.Glob(filepath.Join(groups, "demo", "media", "*", "inline.bin"))
			if err != nil {
				t.Fatal(err)
			}
			if size == 4 && (len(files) != 1 || strings.Contains(msg.Content, "unavailable=")) {
				t.Fatalf("valid attachment: files=%v content=%q", files, msg.Content)
			}
			if size > 4 && (len(files) != 0 || !strings.Contains(msg.Content, "unavailable=")) {
				t.Fatalf("oversized attachment: files=%v content=%q", files, msg.Content)
			}
		})
	}
}
