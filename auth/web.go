package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
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

// 5 attempts per 15 minutes per IP.
var loginLimiter = &struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}{buckets: make(map[string][]time.Time)}

func loginAllowed(ip string) bool {
	const limit = 5
	const window = 15 * time.Minute
	loginLimiter.mu.Lock()
	defer loginLimiter.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	hits := loginLimiter.buckets[ip][:0:0]
	for _, t := range loginLimiter.buckets[ip] {
		if t.After(cutoff) {
			hits = append(hits, t)
		}
	}
	if len(loginLimiter.buckets) > 10000 {
		for k, v := range loginLimiter.buckets {
			if len(v) == 0 || !v[len(v)-1].After(cutoff) {
				delete(loginLimiter.buckets, k)
			}
		}
	}
	if len(hits) >= limit {
		loginLimiter.buckets[ip] = hits
		return false
	}
	loginLimiter.buckets[ip] = append(hits, now)
	return true
}

func wrapOAuth(buttons string) string {
	if buttons == "" {
		return ""
	}
	return `<div class="sep">or</div>` + buttons
}

func handleLoginPage(cfg *core.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		buttons := ""
		if cfg.GoogleClientID != "" {
			buttons += `<a href="/auth/google" class="oauth-btn">Sign in with Google</a>`
		}
		if cfg.GitHubClientID != "" {
			buttons += `<a href="/auth/github" class="oauth-btn">Sign in with GitHub</a>`
		}
		if cfg.DiscordClientID != "" {
			buttons += `<a href="/auth/discord" class="oauth-btn">Sign in with Discord</a>`
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html>
<html><head><title>arizuko — login</title>
<style>
:root{--bg:#0a0a0a;--fg:#e0e0e0;--accent:#4ade80;--accent3:#58a6ff;--dim:#666;--border:#222;--card:#111}
[data-theme=light]{--bg:#fafafa;--fg:#1a1a1a;--accent:#16a34a;--accent3:#0969da;--dim:#888;--border:#ddd;--card:#fff}
*{box-sizing:border-box}
body{font-family:"SF Mono","Fira Code","JetBrains Mono",Consolas,monospace;font-size:14px;display:flex;justify-content:center;align-items:center;min-height:100vh;margin:0;color:var(--fg);background:var(--bg)}
form{background:var(--card);border:1px solid var(--border);padding:2rem;border-radius:6px;width:300px}
h1{margin:0 0 .2em;font-size:1.4em;color:var(--accent);text-align:center}
.sub{color:var(--dim);font-size:.85em;text-align:center;margin:0 0 1.2em}
input{width:100%%;padding:.5rem;margin:.25rem 0 1rem;border:1px solid var(--border);border-radius:4px;background:var(--bg);color:var(--fg);font-family:inherit;font-size:.9em}
input:focus{outline:none;border-color:var(--accent3)}
button{width:100%%;padding:.6rem;background:var(--accent);color:var(--bg);border:none;border-radius:4px;cursor:pointer;font-family:inherit;font-weight:bold;font-size:.9em}
button:hover{opacity:.9}
.sep{color:var(--dim);text-align:center;margin:1em 0 .5em;font-size:.8em}
.oauth-btn{display:block;width:100%%;padding:.55rem;margin-top:.4em;background:var(--bg);color:var(--fg);border:1px solid var(--border);border-radius:4px;text-align:center;text-decoration:none;font-size:.9em}
.oauth-btn:hover{border-color:var(--accent3);color:var(--accent3)}
</style>
<script>(function(){var t=localStorage.getItem('hub-theme')||(matchMedia('(prefers-color-scheme: dark)').matches?'dark':'light');document.documentElement.setAttribute('data-theme',t)})();</script>
</head><body>
<form method="POST" action="/auth/login">
<h1>arizuko</h1>
<p class="sub">sign in</p>
<input name="username" placeholder="username" required autofocus>
<input name="password" type="password" placeholder="password" required>
<button type="submit">login</button>
%s</form></body></html>`, wrapOAuth(buttons))
	}
}

func handleLogin(s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if !loginAllowed(ip) {
			http.Error(w, "too many attempts", http.StatusTooManyRequests)
			return
		}
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
		issueSession(w, r, s, secret, u.Sub, u.Name, secure)
	}
}

func handleRefresh(s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err != nil {
			http.Error(w, "no refresh token", http.StatusUnauthorized)
			return
		}
		h := HashToken(cookie.Value)
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
		issueSession(w, r, s, secret, u.Sub, u.Name, secure)
	}
}

func handleLogout(s *store.Store, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(cookieName)
		if err == nil {
			s.DeleteAuthSession(HashToken(cookie.Value))
		}
		http.SetCookie(w, &http.Cookie{
			Name: cookieName, Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
	}
}

func issueSession(w http.ResponseWriter, r *http.Request, s *store.Store, secret []byte, sub, name string, secure bool) {
	groups := s.UserGroups(sub)
	jwt := mintJWT(secret, sub, name, groups, jwtTTL)
	refresh := genToken()
	exp := time.Now().Add(refreshTTL)
	if err := s.CreateAuthSession(HashToken(refresh), sub, exp); err != nil {
		slog.Error("create session failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: cookieName, Value: refresh, Path: "/",
		Expires: exp, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	dest := "/"
	if c, err := r.Cookie("auth_return"); err == nil && c.Value != "" {
		dest = c.Value
		http.SetCookie(w, &http.Cookie{
			Name: "auth_return", Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		})
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><script>
localStorage.setItem('jwt',%q);window.location=%q;
</script></head><body></body></html>`, jwt, dest)
}

func genToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func HashToken(token string) string {
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
	// $argon2id$v=19$m=..,t=..,p=..$salt$hash → 6 parts (leading empty)
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return nil
	}
	var p argon2Params
	n, _ := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads)
	if n != 3 {
		return nil
	}
	p.salt = parts[4]
	p.hash = parts[5]
	return &p
}
