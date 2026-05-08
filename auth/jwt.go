package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("expired token")
)

type Claims struct {
	Sub    string   `json:"sub"`
	Name   string   `json:"name"`
	Groups []string `json:"groups,omitempty"` // grant patterns; `**` = operator
	Iat    int64    `json:"iat"`
	Nbf    int64    `json:"nbf"`
	Exp    int64    `json:"exp"`
}

const clockSkew = 30 * time.Second

func mintJWT(secret []byte, sub, name string, groups []string, ttl time.Duration) string {
	hdr := base64.RawURLEncoding.EncodeToString(
		[]byte(`{"alg":"HS256","typ":"JWT"}`))
	now := time.Now().Unix()
	c := Claims{
		Sub: sub, Name: name, Groups: groups,
		Iat: now, Nbf: now, Exp: time.Now().Add(ttl).Unix(),
	}
	payload, _ := json.Marshal(c)
	body := base64.RawURLEncoding.EncodeToString(payload)
	sig := sign(secret, hdr+"."+body)
	return hdr + "." + body + "." + sig
}

func VerifyJWT(secret []byte, token string) (Claims, error) {
	parts := strings.SplitN(token, ".", 3)
	if len(parts) != 3 {
		return Claims{}, ErrInvalidToken
	}
	hdrRaw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var hdr struct {
		Alg string `json:"alg"`
		Typ string `json:"typ"`
	}
	if err := json.Unmarshal(hdrRaw, &hdr); err != nil {
		return Claims{}, ErrInvalidToken
	}
	if hdr.Alg != "HS256" || (hdr.Typ != "" && hdr.Typ != "JWT") {
		return Claims{}, ErrInvalidToken
	}
	expected := sign(secret, parts[0]+"."+parts[1])
	if !hmac.Equal([]byte(parts[2]), []byte(expected)) {
		return Claims{}, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Claims{}, ErrInvalidToken
	}
	now := time.Now().Unix()
	skew := int64(clockSkew.Seconds())
	if c.Nbf != 0 && now+skew < c.Nbf {
		return c, ErrInvalidToken
	}
	if c.Iat != 0 && now+skew < c.Iat {
		return c, ErrInvalidToken
	}
	if now > c.Exp {
		return c, ErrExpiredToken
	}
	return c, nil
}

func sign(secret []byte, msg string) string {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(msg))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
