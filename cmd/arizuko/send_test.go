package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

func TestOperatorInjectTopicWait(t *testing.T) {
	if dir := os.Getenv("ARIZUKO_REFUSAL_CHILD"); dir != "" {
		st := mustOpen("routd")
		operatorInject(st, dir, "krons", "operator", "hello", "wanted", true)
		return
	}
	if testing.Short() {
		t.Skip("timed reply regression")
	}
	st := instanceStore(t)
	key, err := auth.NewSigningKey("topic-test")
	if err != nil {
		t.Fatal(err)
	}
	seedAuthDB(t, t.TempDir(), key)
	posted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted <- struct{}{}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	u, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("API_PORT="+u.Port()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestOperatorInjectTopicWait$")
	cmd.Env = append(os.Environ(), "ARIZUKO_REFUSAL_CHILD="+dir)
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-posted:
	case <-ctx.Done():
		t.Fatal("injection did not arrive")
	}
	start := time.Now()
	write := func(id, topic, content string) {
		t.Helper()
		if err := st.PutMessage(core.Message{ID: id, ChatJID: "web:krons", BotMsg: true, FromMe: true, Topic: topic, Content: content, Timestamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 105 {
		write(fmt.Sprintf("foreign-%d", i), "other", "foreign reply")
	}
	if wait := time.Until(start.Add(18 * time.Second)); wait > 0 {
		time.Sleep(wait)
	}
	write("wanted", "wanted", "wanted reply")
	time.Sleep(3 * time.Second)
	write("late-foreign", "other", "late foreign reply")
	err = cmd.Wait()
	elapsed := time.Since(start)
	if err != nil || !strings.Contains(output.String(), "wanted reply") || strings.Contains(output.String(), "foreign reply") || elapsed > 36*time.Second {
		t.Fatalf("err=%v elapsed=%v output=%q", err, elapsed, output.String())
	}
}

// The operator's inject goes to routd's ingest with a bearer routd verifies,
// not into the store: a row written behind routd's back waits until something
// polls the chat, and routd polls nothing (BUGS L821).
func TestOperatorInjectPostsToRoutd(t *testing.T) {
	key, err := auth.NewSigningKey("test-kid")
	if err != nil {
		t.Fatal(err)
	}
	seedAuthDB(t, t.TempDir(), key)
	ks := auth.NewKeySet(map[string]*ecdsa.PublicKey{"test-kid": &key.Priv.PublicKey})
	var got apiv1.Message
	var path string
	var scope []string
	routd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.Method + " " + r.URL.Path
		if s, err := auth.VerifyHTTP(r, ks); err == nil {
			scope = s.Scope
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode: %v", err)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer routd.Close()
	u, _ := url.Parse(routd.URL)
	dataDir := t.TempDir()
	env := "API_PORT=" + u.Port() + "\n"
	if err := os.WriteFile(filepath.Join(dataDir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}

	operatorInject(routdStore(t), dataDir, "krons", "operator", "draft me 3 tweets", "", false)

	if path != "POST /v1/messages" || !slices.Contains(scope, "messages:write") {
		t.Fatalf("routd saw %q with scope %v", path, scope)
	}
	if got.ChatJID != "web:krons" || got.Content != "draft me 3 tweets" || got.Verb != "mention" ||
		got.Sender != "operator" || got.Source != "cli" || !strings.HasPrefix(got.ID, "cli") {
		t.Fatalf("routd got %+v", got)
	}
}
