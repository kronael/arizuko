package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"strings"
)

// verifySignature checks GitHub's X-Hub-Signature-256 ("sha256=<hex>") over the
// verbatim body. GitHub signs no timestamp, so replay defence is the message
// id routd dedups on, not this check.
func verifySignature(secret, header string, body []byte) error {
	if secret == "" {
		return errors.New("webhook secret not configured")
	}
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok || got == "" {
		return errors.New("missing X-Hub-Signature-256")
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(want), []byte(got)) != 1 {
		return errors.New("signature mismatch")
	}
	return nil
}
