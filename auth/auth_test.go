package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func sha256sum(b []byte) [32]byte { return sha256.Sum256(b) }

func hmacSHA256(key, msg []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return hex.EncodeToString(h.Sum(nil))
}

var testSecret = []byte("test-secret-key-for-testing-only")

func TestJWTRoundTrip(t *testing.T) {
	token := mintJWT(testSecret, "user1", "Test User", nil, time.Hour)
	claims, err := VerifyJWT(testSecret, token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Sub != "user1" {
		t.Fatalf("got sub=%q, want user1", claims.Sub)
	}
	if claims.Name != "Test User" {
		t.Fatalf("got name=%q, want Test User", claims.Name)
	}
}

func TestJWTExpired(t *testing.T) {
	token := mintJWT(testSecret, "user1", "Test", nil, -time.Hour)
	_, err := VerifyJWT(testSecret, token)
	if err != ErrExpiredToken {
		t.Fatalf("got err=%v, want ErrExpiredToken", err)
	}
}

// A token expired by less than clockSkew must still be accepted (skew parity
// with the ES256 path in jwks.go).
func TestJWTExpiredWithinSkew(t *testing.T) {
	token := mintJWT(testSecret, "user1", "Test", nil, -10*time.Second)
	_, err := VerifyJWT(testSecret, token)
	if err != nil {
		t.Fatalf("token within clockSkew must be accepted, got err=%v", err)
	}
}

func TestJWTBadSignature(t *testing.T) {
	token := mintJWT(testSecret, "user1", "Test", nil, time.Hour)
	_, err := VerifyJWT([]byte("wrong"), token)
	if err != ErrInvalidToken {
		t.Fatalf("got err=%v, want ErrInvalidToken", err)
	}
}

func TestOAuthStateExpired(t *testing.T) {
	// create state with timestamp 11 minutes in the past
	ts := fmt.Sprintf("%d", time.Now().Add(-11*time.Minute).Unix())
	mac := hmacSHA256(testSecret, []byte(ts))
	state := ts + "." + mac

	r := httptest.NewRequest(
		"GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if _, ok := VerifyState(testSecret, r); ok {
		t.Fatal("expired state should not verify")
	}
}

func TestOAuthStateCookie(t *testing.T) {
	state := SignState(testSecret, StateIntent{})
	if !strings.Contains(state, ".") {
		t.Fatal("state should contain timestamp.signature")
	}
	// simulate verification
	r := httptest.NewRequest("GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if _, ok := VerifyState(testSecret, r); !ok {
		t.Fatal("valid state should verify")
	}
}

func TestTelegramWidgetVerify(t *testing.T) {
	botToken := "123456:ABC-DEF"
	authDate := fmt.Sprintf("%d", time.Now().Unix())
	form := url.Values{
		"id":         {"12345"},
		"first_name": {"Test"},
		"auth_date":  {authDate},
	}
	// compute valid hash
	check := "auth_date=" + authDate + "\nfirst_name=Test\nid=12345"
	secret := sha256sum([]byte(botToken))
	h := hmacSHA256(secret[:], []byte(check))
	form.Set("hash", h)

	if !VerifyTelegramWidget(form, botToken) {
		t.Fatal("valid telegram widget should verify")
	}
	// stale auth_date should fail
	staleForm := url.Values{
		"id":         {"12345"},
		"first_name": {"Test"},
		"auth_date":  {"1234567890"},
	}
	staleCheck := "auth_date=1234567890\nfirst_name=Test\nid=12345"
	staleH := hmacSHA256(secret[:], []byte(staleCheck))
	staleForm.Set("hash", staleH)
	if VerifyTelegramWidget(staleForm, botToken) {
		t.Fatal("stale auth_date should fail")
	}
	form.Set("hash", "invalid")
	if VerifyTelegramWidget(form, botToken) {
		t.Fatal("invalid hash should fail")
	}
}

// --- Identity (4/R: no tier, no world rank on the id) ---

func TestIdentityResolve(t *testing.T) {
	// Only the empty "" folder resolves to root (the operator/service sentinel);
	// every named folder is a plain, non-root identity — authority is its acl rows,
	// not its depth.
	tests := []struct {
		folder string
		isRoot bool
	}{
		{"", true},
		{"main", false},
		{"world/parent", false},
		{"world/a/b/c", false},
	}
	for _, tc := range tests {
		id := Resolve(tc.folder)
		if id.Folder != tc.folder {
			t.Errorf("%s: folder got %q", tc.folder, id.Folder)
		}
		if id.IsRoot != tc.isRoot {
			t.Errorf("%s: isRoot got %v, want %v", tc.folder, id.IsRoot, tc.isRoot)
		}
	}
}
