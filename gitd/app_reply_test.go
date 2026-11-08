package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/chanlib"
	"github.com/kronael/arizuko/githubapp"
	"github.com/kronael/arizuko/store"
)

func TestReplySourceSelection(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	auth := ""
	missing := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/project/installation":
			if missing {
				http.NotFound(w, r)
				return
			}
			fmt.Fprint(w, `{"id":1}`)
		case "/app/installations/1/access_tokens":
			fmt.Fprintf(w, `{"token":"app-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/repos/acme/project/issues/1/comments":
			auth = r.Header.Get("Authorization")
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":1}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	for _, test := range []struct {
		name                   string
		sources                []string
		credentials, noInstall bool
		want                   string
	}{
		{"repo", []string{"repo/acme/project"}, true, false, "Bearer pat"},
		{"org", []string{"org/acme"}, true, false, "Bearer pat"},
		{"overlap", []string{"app/bot", "org/acme", "repo/acme/project"}, true, false, "Bearer pat"},
		{"app", []string{"app/bot"}, true, false, "Bearer app-token"},
		{"missing credentials", []string{"app/bot"}, false, false, ""},
		{"missing installation", []string{"app/bot"}, true, true, ""},
		{"no source", nil, true, false, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := newBot(config{Token: "pat", APIBase: server.URL})
			var sources testSources
			for _, source := range test.sources {
				sources = append(sources, store.GitHubSource{Source: source})
			}
			b.sources = sources
			if test.credentials {
				b.app, err = githubapp.New("123", privateKey, server.URL, server.Client())
				if err != nil {
					t.Fatal(err)
				}
			}
			auth = ""
			missing = test.noInstall
			_, err := b.Send(chanlib.SendRequest{ChatJID: "github:acme/project/issue/1", Content: "reply"})
			if test.want == "" && err == nil || test.want != "" && err != nil {
				t.Fatalf("error = %v", err)
			}
			if auth != test.want {
				t.Errorf("authorization = %q, want %q", auth, test.want)
			}
		})
	}
}

func TestAppSourceTypingMarkUsesInstallationToken(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	auth := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/project/installation":
			fmt.Fprint(w, `{"id":1}`)
		case "/app/installations/1/access_tokens":
			fmt.Fprintf(w, `{"token":"app-token","expires_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
		case "/repos/acme/project/issues/comments/7/reactions":
			auth = r.Header.Get("Authorization")
			w.WriteHeader(201)
			fmt.Fprint(w, `{"id":9}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	b := newBot(config{Token: "pat", APIBase: server.URL})
	b.sources = testSources{{Source: "app/bot"}}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	if b.app, err = githubapp.New("123", privateKey, server.URL, server.Client()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.addEyes("github:acme/project/issue/1", "gh-c7"); err != nil {
		t.Fatal(err)
	}
	if auth != "Bearer app-token" {
		t.Fatalf("authorization = %q, want the installation token", auth)
	}
}
