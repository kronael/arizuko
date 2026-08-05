package store

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// The two encodings must stay one digest: invites/onboarding key TEXT columns
// by TokenRef while route_tokens keys a BLOB column by TokenRefBytes, and
// resreg's route-token export hex-encodes token_hash expecting an invite-style
// ref back. Z3c collapsed the two implementations into one for exactly this;
// this pins that they cannot drift apart again.
func TestTokenRefEncodingsAreOneDigest(t *testing.T) {
	for _, token := range []string{"", "a", "deadbeef", "tok-with-üñî©ode", string(make([]byte, 4096))} {
		raw := TokenRefBytes(token)
		if len(raw) != sha256.Size {
			t.Fatalf("TokenRefBytes(%q) = %d bytes, want %d", token, len(raw), sha256.Size)
		}
		if got, want := hex.EncodeToString(raw), TokenRef(token); got != want {
			t.Errorf("hex(TokenRefBytes(%q)) = %s, want TokenRef = %s", token, got, want)
		}
		back, err := hex.DecodeString(TokenRef(token))
		if err != nil {
			t.Fatalf("TokenRef(%q) is not hex: %v", token, err)
		}
		if string(back) != string(raw) {
			t.Errorf("unhex(TokenRef(%q)) != TokenRefBytes", token)
		}
	}
}

// Full digest, never truncated — a prefix could resolve the wrong row.
func TestTokenRefIsFullSHA256(t *testing.T) {
	const token = "some-bearer-token"
	want := sha256.Sum256([]byte(token))
	if got := TokenRef(token); got != hex.EncodeToString(want[:]) {
		t.Errorf("TokenRef = %q, want %q", got, hex.EncodeToString(want[:]))
	}
	if got := len(TokenRef(token)); got != sha256.Size*2 {
		t.Errorf("TokenRef length = %d, want %d", got, sha256.Size*2)
	}
}

// Stored values must be byte-identical to what the pre-Z3c helpers produced —
// this is a rename, not a re-hash. Vectors computed from the old InviteRef /
// HashRouteToken implementations.
func TestTokenRefMatchesPreRenameVectors(t *testing.T) {
	for token, want := range map[string]string{
		"":      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
		"abc":   "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
		"token": "3c469e9d6c5875d37a43f353d4f88e61fcf812c66eee3457465a40b0da4153e0",
	} {
		if got := TokenRef(token); got != want {
			t.Errorf("TokenRef(%q) = %s, want %s", token, got, want)
		}
		if got := hex.EncodeToString(TokenRefBytes(token)); got != want {
			t.Errorf("TokenRefBytes(%q) = %s, want %s", token, got, want)
		}
	}
}
