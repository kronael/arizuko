package main

import (
	"crypto/ecdsa"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kronael/arizuko/auth"
)

// onbod dual-accept (spec 5/1 § cutover): the stripUnsigned gate accepts an
// authd-minted ES256 bearer alongside the live HMAC X-User-Sig when AUTHD_URL
// is set. Unset → nil KeySet → StripUnsigned, exactly as before. onbod's
// handlers read X-User-Sub, so the gate must stamp it on bearer success.

func onbodBearer(t *testing.T) (*auth.KeySet, string) {
	t.Helper()
	k, err := auth.NewSigningKey("k1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := k.Sign(auth.TokenClaims{Sub: "google:123", Typ: "user"}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return auth.NewKeySet(map[string]*ecdsa.PublicKey{"k1": &k.Priv.PublicKey}), tok
}

const onbodHMACSecret = "onbod-test-secret"

func TestOnbodStripUnsigned_ES256Bearer_StampsSub(t *testing.T) {
	ks, tok := onbodBearer(t)
	gate := auth.StripUnsignedOrBearer(onbodHMACSecret, ks)
	var seenSub string
	h := gate(func(w http.ResponseWriter, r *http.Request) {
		seenSub = r.Header.Get("X-User-Sub")
	})
	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seenSub != "google:123" {
		t.Fatalf("ES256 bearer not stamped into X-User-Sub: got %q", seenSub)
	}
}

func TestOnbodStripUnsigned_HMAC_StillWorks(t *testing.T) {
	ks, _ := onbodBearer(t)
	gate := auth.StripUnsignedOrBearer(onbodHMACSecret, ks)
	var seenSub string
	h := gate(func(w http.ResponseWriter, r *http.Request) {
		seenSub = r.Header.Get("X-User-Sub")
	})
	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("X-User-Sub", "github:42")
	r.Header.Set("X-User-Sig", auth.SignHMAC(onbodHMACSecret, auth.UserSigMessage("github:42", "", "")))
	w := httptest.NewRecorder()
	h(w, r)
	if seenSub != "github:42" {
		t.Fatalf("HMAC-signed sub stripped with ES256 KeySet present: got %q", seenSub)
	}
}

func TestOnbodStripUnsigned_NilKeySet_StripsUnsigned(t *testing.T) {
	_, tok := onbodBearer(t)
	gate := auth.StripUnsignedOrBearer(onbodHMACSecret, nil) // AUTHD_URL unset
	var seenSub string
	h := gate(func(w http.ResponseWriter, r *http.Request) {
		seenSub = r.Header.Get("X-User-Sub")
	})
	r := httptest.NewRequest("GET", "/onboard", nil)
	r.Header.Set("X-User-Sub", "github:hacker") // forged unsigned
	r.Header.Set("Authorization", "Bearer "+tok)
	w := httptest.NewRecorder()
	h(w, r)
	if seenSub != "" {
		t.Fatalf("nil KeySet must equal StripUnsigned: unsigned sub not stripped, got %q", seenSub)
	}
}
