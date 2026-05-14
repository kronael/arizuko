package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/store"
	"github.com/kronael/arizuko/theme"
	"golang.org/x/crypto/argon2"
)

const (
	jwtTTL     = time.Hour
	refreshTTL = 30 * 24 * time.Hour
	cookieName = "refresh_token"
)

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
		if buttons != "" {
			buttons = `<div class="sep">or</div>` + buttons
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html>%s<body>
<div class="page-center">
<form method="POST" action="/auth/login" class="card card-sm" style="padding:2rem">
<h1 style="text-align:center;margin-bottom:.1em">arizuko</h1>
<p class="sub">Claude agent gateway &middot; sign in</p>
<input name="username" placeholder="username" required autofocus style="margin-bottom:.5rem">
<input name="password" type="password" placeholder="password" required style="margin-bottom:1rem">
<button type="submit" style="width:100%%">login</button>
%s</form></div></body></html>`, theme.Head("login"), buttons)
	}
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i >= 0 {
			xff = xff[:i]
		}
		if ip := strings.TrimSpace(xff); ip != "" {
			return ip
		}
	}
	if xr := strings.TrimSpace(r.Header.Get("X-Real-IP")); xr != "" {
		return xr
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

func handleLogin(s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := clientIP(r)
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
	sub = s.CanonicalSub(sub)
	groups := s.UserScopes(sub)
	jwt := mintJWT(secret, sub, name, groups, jwtTTL)
	refresh, err := genToken()
	if err != nil {
		slog.Error("generate refresh token failed", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
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
	if len(groups) == 0 {
		dest = "/onboard"
	}
	if c, err := r.Cookie("auth_return"); err == nil && c.Value != "" {
		if safe, ok := safeReturn(c.Value); ok {
			dest = safe
		}
		http.SetCookie(w, &http.Cookie{
			Name: "auth_return", Value: "", Path: "/",
			MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
		})
	}
	jwtJS, _ := json.Marshal(jwt)
	destJS, _ := json.Marshal(dest)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><script>
localStorage.setItem('jwt',%s);window.location=%s;
</script></head><body></body></html>`,
		jsSafe(jwtJS), jsSafe(destJS))
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

func genToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
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
