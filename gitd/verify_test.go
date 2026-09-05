package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifySignature(t *testing.T) {
	body := []byte(`{"zen":"Keep it logically awesome.","hook_id":1}`)
	good := sign("s3cret", body)
	if err := verifySignature("s3cret", good, body); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	cases := map[string]struct {
		secret, header string
		body           []byte
	}{
		"wrong secret":   {"other", good, body},
		"tampered body":  {"s3cret", good, append([]byte(nil), body[:len(body)-1]...)},
		"forged hex":     {"s3cret", "sha256=deadbeef", body},
		"missing header": {"s3cret", "", body},
		"sha1 prefix":    {"s3cret", "sha1=" + good[len("sha256="):], body},
		"bare hex":       {"s3cret", good[len("sha256="):], body},
		"empty secret":   {"", sign("", body), body},
	}
	for name, c := range cases {
		if err := verifySignature(c.secret, c.header, c.body); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
