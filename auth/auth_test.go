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

func hmacSHA256(key, msg []byte) string {
	h := hmac.New(sha256.New, key)
	h.Write(msg)
	return hex.EncodeToString(h.Sum(nil))
}

var testSecret = []byte("test-secret-key-for-testing-only")

func TestOAuthStateExpired(t *testing.T) {
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
	r := httptest.NewRequest("GET", "/callback?state="+url.QueryEscape(state), nil)
	r.AddCookie(&http.Cookie{Name: "oauth_state", Value: state})
	if _, ok := VerifyState(testSecret, r); !ok {
		t.Fatal("valid state should verify")
	}
}

// Identity (5/33-paths-roles: no tier, no world rank on the id)

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
