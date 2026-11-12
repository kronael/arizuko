package main

import (
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/kronael/arizuko/auth"
	apiv1 "github.com/kronael/arizuko/routd/api/v1"
)

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
