package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
	"golang.org/x/crypto/argon2"
)

const (
	jwtTTL     = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	cookieName = "refresh_token"
)

func handleLoginPage(cfg *core.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		buttons := ""
		if cfg.GoogleClientID != "" {
			buttons += `<a href="/auth/google" class="oauth-btn">Sign in with Google</a>`
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>Login</title>
<style>
body{font-family:system-ui;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;background:#f5f5f5}
form{background:#fff;padding:2rem;border-radius:8px;box-shadow:0 2px 8px rgba(0,0,0,.1);width:300px}
input{width:100%%;padding:.5rem;margin:.25rem 0 1rem;box-sizing:border-box;border:1px solid #ddd;border-radius:4px}
button{width:100%%;padding:.5rem;background:#333;color:#fff;border:none;border-radius:4px;cursor:pointer}
.oauth-btn{display:block;width:100%%;padding:.5rem;margin-top:.5rem;background:#fff;color:#333;border:1px solid #ddd;border-radius:4px;cursor:pointer;text-align:center;text-decoration:none;box-sizing:border-box}
h2{margin:0 0 1rem;text-align:center}
</style></head><body>
<form method="POST" action="/auth/login">
<h2>Login</h2>
<input name="username" placeholder="Username" required autofocus>
<input name="password" type="password" placeholder="Password" required>
<button type="submit">Login</button>
%s</form></body></html>`, buttons)
	}
}

func handleLogin(s *store.Store, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		username := r.FormValue("username")
		password := r.FormValue("password")
		if username == "" || password == "" {
			http.Error(w, "missing credentials", http.StatusBadRequest)
			return
		}
		u, ok := s.AuthUserByUsername(username)
		if !ok {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		if !verifyArgon2(u.Hash, password) {
			http.Error(w, "invalid credentials", http.StatusUnauthorized)
			return
		}
		issueSession(w, s, secret, u.Sub, u.Name)
	}
}

func handleRefresh(s *store.Store, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil {
			http.Error(w, "no refresh token", http.StatusUnauthorized)
			return
		}
		h := hashToken(cookie.Value)
		sess, ok := s.AuthSession(h)
		if !ok || time.Now().After(sess.ExpiresAt) {
			http.Error(w, "invalid session", http.StatusUnauthorized)
			return
		}
		s.DeleteAuthSession(h)
		u, ok := s.AuthUserBySub(sess.UserSub)
		if !ok {
			http.Error(w, "user not found", http.StatusUnauthorized)
			return
		}
		issueSession(w, s, secret, u.Sub, u.Name)
	}
}

func handleLogout(s *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err == nil {
			s.DeleteAuthSession(hashToken(cookie.Value))
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	}
}

func issueSession(w http.ResponseWriter, s *store.Store, secret []byte, sub, name string) {
	jwt := mintJWT(secret, sub, name, jwtTTL)
	refresh := genToken()
	exp := time.Now().Add(refreshTTL)
	if err := s.CreateAuthSession(hashToken(refresh), sub, exp); err != nil {
		slog.Error("create session failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: refresh, Path: "/",
		Expires: exp, HttpOnly: true, SameSite: http.SameSiteLaxMode,
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><script>
localStorage.setItem('jwt','%s');window.location='/';
</script></head><body></body></html>`, jwt)
}

func genToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:])
}

func verifyArgon2(encoded, password string) bool {
	// format: $argon2id$v=19$m=65536,t=3,p=4$salt$hash
	parts := splitArgon2(encoded)
	if parts == nil {
		return false
	}
	salt, _ := base64.RawStdEncoding.DecodeString(parts.salt)
	expected, _ := base64.RawStdEncoding.DecodeString(parts.hash)
	derived := argon2.IDKey(
		[]byte(password), salt,
		parts.time, parts.memory, parts.threads,
		uint32(len(expected)),
	)
	return subtle.ConstantTimeCompare(derived, expected) == 1
}

type argon2Params struct {
	memory  uint32
	time    uint32
	threads uint8
	salt    string
	hash    string
}

func splitArgon2(encoded string) *argon2Params {
	// $argon2id$v=19$m=65536,t=3,p=4$salt$hash
	var p argon2Params
	n, _ := fmt.Sscanf(encoded, "$argon2id$v=19$m=%d,t=%d,p=%d$",
		&p.memory, &p.time, &p.threads)
	if n != 3 {
		return nil
	}
	// find salt and hash after the params
	idx := 0
	dollars := 0
	for i, c := range encoded {
		if c == '$' {
			dollars++
			if dollars == 4 {
				idx = i + 1
				break
			}
		}
	}
	rest := encoded[idx:]
	for i, c := range rest {
		if c == '$' {
			p.salt = rest[:i]
			p.hash = rest[i+1:]
			return &p
		}
	}
	return nil
}
