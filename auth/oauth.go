package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kronael/arizuko/core"
)

const stateTTL = 10 * time.Minute

var googleTokenURL = "https://oauth2.googleapis.com/token"
var googleUserinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"

var httpClient = &http.Client{Timeout: 15 * time.Second}

func postForm(ctx context.Context, url string, data url.Values) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", url, strings.NewReader(data.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return httpClient.Do(req)
}

func consumePKCE(w http.ResponseWriter, r *http.Request, secure bool) string {
	c, err := r.Cookie("oauth_pkce")
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_pkce", Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	return c.Value
}

func exchangeGoogle(ctx context.Context, cfg *core.Config, code, verifier string) (string, error) {
	cb := authBaseURL(cfg) + "/auth/google/callback"
	form := url.Values{
		"code":          {code},
		"client_id":     {cfg.GoogleClientID},
		"client_secret": {cfg.GoogleSecret},
		"redirect_uri":  {cb},
		"grant_type":    {"authorization_code"},
	}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	resp, err := postForm(ctx, googleTokenURL, form)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("empty access token")
	}
	return tok.AccessToken, nil
}

func fetchGoogleUser(ctx context.Context, token string) (sub, name, email string, verified bool, err error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", googleUserinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", "", false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", "", false, fmt.Errorf("google userinfo: %s", resp.Status)
	}
	var u struct {
		Sub           string `json:"sub"`
		Name          string `json:"name"`
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return "", "", "", false, err
	}
	if u.Sub == "" {
		return "", "", "", false, fmt.Errorf("google userinfo: empty sub")
	}
	return u.Sub, u.Name, u.Email, u.EmailVerified, nil
}

func matchEmailAllowlist(email, allowlist string) bool {
	for _, pat := range strings.Split(allowlist, ",") {
		pat = strings.TrimSpace(pat)
		if pat == "" {
			continue
		}
		if matched, _ := filepath.Match(pat, email); matched {
			return true
		}
	}
	return false
}

func checkGitHubOrgMember(ctx context.Context, token, org, username string) bool {
	u := fmt.Sprintf("https://api.github.com/orgs/%s/members/%s",
		url.PathEscape(org), url.PathEscape(username))
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := httpClient.Do(req)
	if err != nil {
		slog.Warn("github org check failed", "org", org, "err", err)
		return false
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusNoContent
}

type stateIntent struct {
	Intent   string `json:"i,omitempty"`
	LinkFrom string `json:"f,omitempty"`
	Return   string `json:"r,omitempty"`
}

func signStateP(secret []byte, p stateIntent) string {
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	nonceB := make([]byte, 16)
	_, _ = rand.Read(nonceB)
	nonce := base64.RawURLEncoding.EncodeToString(nonceB)
	signed := ts + "." + nonce
	if p.Intent != "" || p.LinkFrom != "" || p.Return != "" {
		raw, _ := json.Marshal(p)
		signed += "." + base64.RawURLEncoding.EncodeToString(raw)
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signed))
	sig := hex.EncodeToString(mac.Sum(nil))
	return signed + "." + sig
}

func verifyState(secret []byte, r *http.Request) (stateIntent, bool) {
	var empty stateIntent
	cookie, err := r.Cookie("oauth_state")
	if err != nil {
		return empty, false
	}
	state := r.URL.Query().Get("state")
	if state == "" || state != cookie.Value {
		return empty, false
	}
	parts := strings.Split(state, ".")
	var ts, signed, sig, payload string
	switch len(parts) {
	case 2:
		ts, sig = parts[0], parts[1]
		signed = ts
	case 3:
		ts, sig = parts[0], parts[2]
		signed = ts + "." + parts[1]
	case 4:
		ts, sig = parts[0], parts[3]
		payload = parts[2]
		signed = ts + "." + parts[1] + "." + payload
	default:
		return empty, false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(signed))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return empty, false
	}
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return empty, false
	}
	age := time.Since(time.Unix(tsInt, 0))
	if age < 0 || age >= stateTTL {
		return empty, false
	}
	if payload != "" {
		raw, err := base64.RawURLEncoding.DecodeString(payload)
		if err != nil {
			return empty, false
		}
		var p stateIntent
		if err := json.Unmarshal(raw, &p); err != nil {
			return empty, false
		}
		return p, true
	}
	return empty, true
}

func verifyTelegramWidget(form url.Values, botToken string) bool {
	hash := form.Get("hash")
	if hash == "" {
		return false
	}
	authDate, err := strconv.ParseInt(form.Get("auth_date"), 10, 64)
	if err != nil || authDate <= 0 {
		return false
	}
	age := time.Since(time.Unix(authDate, 0))
	if age > 5*time.Minute || age < -30*time.Second {
		return false
	}
	var keys []string
	for k := range form {
		if k != "hash" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+form.Get(k))
	}
	check := strings.Join(parts, "\n")
	secret := sha256.Sum256([]byte(botToken))
	mac := hmac.New(sha256.New, secret[:])
	mac.Write([]byte(check))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(hash), []byte(expected))
}

func authBaseURL(cfg *core.Config) string {
	return strings.TrimRight(cfg.AuthBaseURL, "/")
}

func exchangeGitHub(ctx context.Context, cfg *core.Config, code, verifier string) (string, error) {
	data := url.Values{
		"client_id":     {cfg.GitHubClientID},
		"client_secret": {cfg.GitHubSecret},
		"code":          {code},
	}
	if verifier != "" {
		data.Set("code_verifier", verifier)
	}
	req, _ := http.NewRequestWithContext(ctx, "POST",
		"https://github.com/login/oauth/access_token",
		strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	if result.Error != "" {
		return "", fmt.Errorf("github: %s", result.Error)
	}
	return result.AccessToken, nil
}

func fetchGitHubUser(ctx context.Context, token string) (string, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("github user: %s", resp.Status)
	}
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return "", "", err
	}
	if u.Login == "" {
		return "", "", fmt.Errorf("github user: empty login")
	}
	name := u.Name
	if name == "" {
		name = u.Login
	}
	return u.Login, name, nil
}

func exchangeDiscord(ctx context.Context, cfg *core.Config, code, verifier string) (string, error) {
	cb := authBaseURL(cfg) + "/auth/discord/callback"
	data := url.Values{
		"client_id":     {cfg.DiscordClientID},
		"client_secret": {cfg.DiscordSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cb},
	}
	if verifier != "" {
		data.Set("code_verifier", verifier)
	}
	resp, err := postForm(ctx, "https://discord.com/api/oauth2/token", data)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("discord token: %s", body)
	}
	var result struct {
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result)
	return result.AccessToken, nil
}

func fetchDiscordUser(ctx context.Context, token string) (string, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET",
		"https://discord.com/api/users/@me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := httpClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("discord user: %s", resp.Status)
	}
	var u struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Global   string `json:"global_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&u); err != nil {
		return "", "", err
	}
	if u.ID == "" {
		return "", "", fmt.Errorf("discord user: empty id")
	}
	name := u.Global
	if name == "" {
		name = u.Username
	}
	return u.ID, name, nil
}

// Exported provider primitives. authd's /auth/* handlers (specs/5/1) own the
// OAuth flow and call these wrappers to reuse the exchange + userinfo code
// without forking it. They are pure (store-free, secret-free); the issuance
// (ES256 + authd refresh store) lives in authd.

func ExchangeGoogle(ctx context.Context, cfg *core.Config, code, verifier string) (string, error) {
	return exchangeGoogle(ctx, cfg, code, verifier)
}

func FetchGoogleUser(ctx context.Context, token string) (sub, name, email string, verified bool, err error) {
	return fetchGoogleUser(ctx, token)
}

func ExchangeGitHub(ctx context.Context, cfg *core.Config, code, verifier string) (string, error) {
	return exchangeGitHub(ctx, cfg, code, verifier)
}

func FetchGitHubUser(ctx context.Context, token string) (sub, name string, err error) {
	return fetchGitHubUser(ctx, token)
}

func ExchangeDiscord(ctx context.Context, cfg *core.Config, code, verifier string) (string, error) {
	return exchangeDiscord(ctx, cfg, code, verifier)
}

func FetchDiscordUser(ctx context.Context, token string) (sub, name string, err error) {
	return fetchDiscordUser(ctx, token)
}

func VerifyTelegramWidget(form url.Values, botToken string) bool {
	return verifyTelegramWidget(form, botToken)
}

func MatchEmailAllowlist(email, allowlist string) bool { return matchEmailAllowlist(email, allowlist) }

func CheckGitHubOrgMember(ctx context.Context, token, org, username string) bool {
	return checkGitHubOrgMember(ctx, token, org, username)
}

func AuthBaseURL(cfg *core.Config) string { return authBaseURL(cfg) }

func SafeReturn(v string) (string, bool) { return safeReturn(v) }

func JSSafe(b []byte) string { return jsSafe(b) }

// SignState / VerifyState expose the signed-cookie CSRF+PKCE state used by the
// provider redirect/callback. authd reuses them for its /auth/* flow with a
// CSRF-only HMAC key (the state is a CSRF token, not an identity token, so it
// stays symmetric — specs/5/1 collide-token rule). intent carries link-intent
// + return path across the redirect.
type StateIntent = stateIntent

func SignState(secret []byte, intent StateIntent) string { return signStateP(secret, intent) }

func VerifyState(secret []byte, r *http.Request) (StateIntent, bool) { return verifyState(secret, r) }

// NewLinkIntent builds a link-intent state payload for ?intent=link flows.
func NewLinkIntent(linkFrom, returnTo string) StateIntent {
	return stateIntent{Intent: "link", LinkFrom: linkFrom, Return: returnTo}
}

// ConsumePKCE reads + clears the PKCE verifier cookie (callback side).
func ConsumePKCE(w http.ResponseWriter, r *http.Request, secure bool) string {
	return consumePKCE(w, r, secure)
}

// WritePKCE generates a PKCE verifier, stores it in an HttpOnly cookie, and
// returns the S256 code_challenge to append to the authorize URL.
func WritePKCE(w http.ResponseWriter, secure bool) (challenge string, err error) {
	vb := make([]byte, 32)
	if _, err := rand.Read(vb); err != nil {
		return "", err
	}
	verifier := base64.RawURLEncoding.EncodeToString(vb)
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_pkce", Value: verifier, Path: "/",
		MaxAge: int(stateTTL.Seconds()), HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode,
	})
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

// WriteStateCookie persists the signed CSRF state in an HttpOnly cookie (the
// redirect side; VerifyState reads it back at callback).
func WriteStateCookie(w http.ResponseWriter, state string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name: "oauth_state", Value: state, Path: "/",
		MaxAge: int(stateTTL.Seconds()), HttpOnly: true,
		Secure: secure, SameSite: http.SameSiteLaxMode,
	})
}
