package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
)

func HashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func safeReturn(v string) (string, bool) {
	if v == "" || v[0] != '/' {
		return "", false
	}
	if strings.HasPrefix(v, "//") || strings.HasPrefix(v, "/\\") {
		return "", false
	}
	if strings.ContainsAny(v, "\\\r\n\x00") {
		return "", false
	}
	u, err := url.Parse(v)
	if err != nil || u.Scheme != "" || u.Host != "" {
		return "", false
	}
	return v, true
}

// jsSafe: U+2028/U+2029 and "</" break out of <script> JSON — escape them.
func jsSafe(b []byte) string {
	s := string(b)
	s = strings.ReplaceAll(s, "\u2028", `\u2028`)
	s = strings.ReplaceAll(s, "\u2029", `\u2029`)
	s = strings.ReplaceAll(s, "</", `<\/`)
	return s
}
