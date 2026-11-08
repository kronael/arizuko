package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/githubapp"
)

func TestWebhookInvalidatesInstallationTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
	for _, test := range []struct {
		event, action, secret string
		wantCode, wantMints   int
	}{
		{"installation_repositories", "removed", "secret", 200, 2},
		{"installation", "deleted", "secret", 200, 2},
		{"installation", "suspend", "secret", 200, 2},
		{"installation_repositories", "added", "secret", 200, 1},
		{"installation", "unknown", "secret", 200, 1},
		{"installation_repositories", "removed", "wrong", 401, 1},
	} {
		t.Run(test.event+"/"+test.action+"/"+test.secret, func(t *testing.T) {
			mints := map[string]int{}
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/acme/one/installation", "/repos/acme/two/installation":
					fmt.Fprint(w, `{"id":42}`)
				case "/app/installations/42/access_tokens":
					var body struct {
						Repositories []string `json:"repositories"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Repositories) != 1 {
						t.Errorf("token request: %+v, %v", body, err)
						w.WriteHeader(400)
						return
					}
					repo := body.Repositories[0]
					mints[repo]++
					fmt.Fprintf(w, `{"token":%q,"expires_at":%q}`, fmt.Sprint(repo, mints[repo]),
						time.Now().Add(time.Hour).Format(time.RFC3339))
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer api.Close()
			e := newTestEnv(t, secretMap{"app/bot": "secret"})
			e.srv.bot.app, err = githubapp.New("123", privateKey, api.URL, api.Client())
			if err != nil {
				t.Fatal(err)
			}
			for _, repo := range []string{"one", "two"} {
				if _, err := e.srv.bot.app.Token(t.Context(), "acme", repo); err != nil {
					t.Fatal(err)
				}
			}
			payload := fmt.Appendf(nil, `{"action":%q,"installation":{"id":42},`+
				`"sender":{"type":"Bot","login":"app-bot"},`+
				`"repositories_removed":[{"full_name":"ACME/One"}]}`, test.action)
			w := e.deliver(t, "/github/app/bot", test.secret, test.event, "revocation", payload)
			if w.Code != test.wantCode {
				t.Fatalf("webhook = %d: %s", w.Code, w.Body.String())
			}
			for _, repo := range []string{"one", "two"} {
				if _, err := e.srv.bot.app.Token(t.Context(), "acme", repo); err != nil {
					t.Fatal(err)
				}
			}
			if mints["one"] != test.wantMints {
				t.Fatalf("removed repository token mints = %d, want %d", mints["one"], test.wantMints)
			}
			wantTwo := 1
			if test.event == "installation" && test.wantMints == 2 {
				wantTwo = 2
			}
			if mints["two"] != wantTwo {
				t.Fatalf("other repository token mints = %d, want %d", mints["two"], wantTwo)
			}
			if len(e.routd.messages()) != 0 {
				t.Fatal("installation event started conversational dispatch")
			}
		})
	}
}
