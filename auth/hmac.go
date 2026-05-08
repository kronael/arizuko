package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

func SignHMAC(secret, msg string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(msg))
	return hex.EncodeToString(h.Sum(nil))
}

func VerifyHMAC(secret, msg, sig string) bool {
	if secret == "" || sig == "" {
		return false
	}
	return hmac.Equal([]byte(SignHMAC(secret, msg)), []byte(sig))
}

func UserSigMessage(sub, name, groupsJSON string) string {
	return "user:" + sub + "|" + name + "|" + groupsJSON
}

func SlinkSigMessage(token, folder string) string {
	return "slink:" + token + "|" + folder
}

func VerifyUserSig(secret string, r *http.Request) bool {
	sub := r.Header.Get("X-User-Sub")
	if sub == "" {
		return false
	}
	return VerifyHMAC(secret, UserSigMessage(sub, r.Header.Get("X-User-Name"), r.Header.Get("X-User-Groups")), r.Header.Get("X-User-Sig"))
}

func VerifySlinkSig(secret string, r *http.Request) bool {
	token := r.Header.Get("X-Slink-Token")
	folder := r.Header.Get("X-Folder")
	if token == "" || folder == "" {
		return false
	}
	return VerifyHMAC(secret, SlinkSigMessage(token, folder), r.Header.Get("X-Slink-Sig"))
}
