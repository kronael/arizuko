package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// SignHMAC returns hex-encoded HMAC-SHA256 of msg under secret.
func SignHMAC(secret, msg string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

// VerifyHMAC constant-time checks sig against SignHMAC(secret, msg).
func VerifyHMAC(secret, msg, sig string) bool {
	if secret == "" || sig == "" {
		return false
	}
	return hmac.Equal([]byte(SignHMAC(secret, msg)), []byte(sig))
}

// UserSigMessage canonicalizes the identity-headers signing payload.
func UserSigMessage(sub, name, groupsJSON string) string {
	return "user:" + sub + "|" + name + "|" + groupsJSON
}

// SlinkSigMessage canonicalizes the slink-token signing payload.
func SlinkSigMessage(token, folder string) string {
	return "slink:" + token + "|" + folder
}

// VerifyUserSig checks X-User-Sig matches the canonical identity headers.
func VerifyUserSig(secret string, r *http.Request) bool {
	sub := r.Header.Get("X-User-Sub")
	if sub == "" {
		return false
	}
	msg := UserSigMessage(sub, r.Header.Get("X-User-Name"), r.Header.Get("X-User-Groups"))
	return VerifyHMAC(secret, msg, r.Header.Get("X-User-Sig"))
}

// VerifySlinkSig checks X-Slink-Sig against the slink token+folder headers.
func VerifySlinkSig(secret string, r *http.Request) bool {
	token := r.Header.Get("X-Slink-Token")
	folder := r.Header.Get("X-Folder")
	if token == "" || folder == "" {
		return false
	}
	return VerifyHMAC(secret, SlinkSigMessage(token, folder), r.Header.Get("X-Slink-Sig"))
}
