package githubapp

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRepositoryTokens(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), ".")
		if len(parts) != 3 {
			t.Error("missing JWT")
			http.Error(w, "jwt", 401)
			return
		}
		signature, err := base64.RawURLEncoding.DecodeString(parts[2])
		hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
		if err != nil || rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, hash[:], signature) != nil {
			t.Error("bad JWT signature")
		}
		claims, err := base64.RawURLEncoding.DecodeString(parts[1])
		var payload struct {
			Iss string `json:"iss"`
			Iat int64  `json:"iat"`
			Exp int64  `json:"exp"`
		}
		if err != nil || json.Unmarshal(claims, &payload) != nil || payload.Iss != "123" || payload.Iat != now.Add(-time.Minute).Unix() || payload.Exp != now.Add(9*time.Minute).Unix() {
			t.Errorf("bad JWT claims: %s", claims)
		}
		switch r.URL.Path {
		case "/repos/acme/one/installation", "/repos/acme/two/installation":
			fmt.Fprint(w, `{"id":42}`)
		case "/app/installations/42/access_tokens":
			var body struct {
				Repositories []string `json:"repositories"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			expected := "one"
			if calls == 2 {
				expected = "two"
			}
			if !reflect.DeepEqual(body.Repositories, []string{expected}) {
				t.Errorf("repository limit = %v", body.Repositories)
			}
			calls++
			fmt.Fprintf(w, `{"token":"token%d","expires_at":%q}`, calls, now.Add(time.Hour).Format(time.RFC3339))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	client, err := New("123", privateKey, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	client.now = func() time.Time { return now }
	for _, expected := range []string{"token1", "token1"} {
		got, err := client.Token(context.Background(), "acme", "one")
		if err != nil || got != expected {
			t.Fatalf("token = %q, %v", got, err)
		}
	}
	now = now.Add(56 * time.Minute)
	got, err := client.Token(context.Background(), "acme", "one")
	if err != nil || got != "token2" {
		t.Fatalf("refresh = %q, %v", got, err)
	}
	got, err = client.Token(context.Background(), "acme", "two")
	if err != nil || got != "token3" {
		t.Fatalf("second repository = %q, %v", got, err)
	}
}
