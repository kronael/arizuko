package main

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/githubapp"
	"github.com/kronael/arizuko/store"
)

func TestAppOnlyStartupWebhookTriggersSeededRoute(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/app" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		jwt := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		parts := strings.Split(jwt, ".")
		if len(parts) != 3 {
			t.Error("missing app JWT")
			w.WriteHeader(401)
			return
		}
		sig, err := base64.RawURLEncoding.DecodeString(parts[2])
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], sig); err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		fmt.Fprint(w, `{"slug":"test-app"}`)
	}))
	defer api.Close()
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	cfg := config{Name: "github", AppID: "123", APIBase: api.URL}
	b := newBot(cfg)
	b.app, err = githubapp.New(cfg.AppID, privateKey, api.URL, api.Client())
	if err != nil {
		t.Fatal(err)
	}
	verifyToken(t.Context(), b, &capRecorder{}, time.Hour)
	routd := &fakeRoutd{}
	upstream := httptest.NewServer(routd)
	defer upstream.Close()
	s := newServer(cfg, b, secretMap{"app/test-app": "secret"}, nil, chanlib.NewRouterClient(upstream.URL))
	payload := bytes.ReplaceAll(fixture(t, "issue_comment_created"), []byte("@arizuko"), []byte("@test-app[bot]"))
	req := httptest.NewRequest("POST", "/github/app/test-app", bytes.NewReader(payload))
	req.SetPathValue("source", "app/test-app")
	req.Header.Set("X-Hub-Signature-256", sign("secret", payload))
	req.Header.Set("X-GitHub-Event", "issue_comment")
	req.Header.Set("X-GitHub-Delivery", "test")
	w := httptest.NewRecorder()
	s.handleWebhook(w, req)
	if w.Code != 200 {
		t.Fatalf("webhook = %d: %s", w.Code, w.Body.String())
	}
	msgs := routd.messages()
	if len(msgs) != 1 {
		t.Fatalf("messages = %d", len(msgs))
	}
	routes, err := store.GitHubSourceRoutes("app/test-app", "eng")
	if err != nil {
		t.Fatal(err)
	}
	if got := resolve(routes, msgs[0]); got.Folder != "eng" || got.Mode != "" {
		t.Errorf("route = %+v, verb = %q, want trigger to eng", got, msgs[0].Verb)
	}
	if calls != 1 {
		t.Errorf("app requests = %d, want 1", calls)
	}
}
