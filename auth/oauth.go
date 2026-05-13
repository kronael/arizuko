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
	"github.com/kronael/arizuko/store"
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

func oauthRedirect(s *store.Store, secret []byte, secure bool, authURL string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intent := stateIntent{}
		if r.URL.Query().Get("intent") == "link" {
			intent.Intent = "link"
			intent.LinkFrom = currentSub(s, secret, r)
			if rt := r.URL.Query().Get("return"); rt != "" {
				if safe, ok := safeReturn(rt); ok {
					intent.Return = safe
				}
			}
			if intent.LinkFrom == "" {
				intent.Intent = ""
			}
		}
		state := signStateP(secret, intent)
		http.SetCookie(w, &http.Cookie{
			Name: "oauth_state", Value: state, Path: "/",
			MaxAge: int(stateTTL.Seconds()), HttpOnly: true,
			Secure: secure, SameSite: http.SameSiteLaxMode,
		})
		vb := make([]byte, 32)
		if _, err := rand.Read(vb); err != nil {
			slog.Error("pkce gen failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		verifier := base64.RawURLEncoding.EncodeToString(vb)
		http.SetCookie(w, &http.Cookie{
			Name: "oauth_pkce", Value: verifier, Path: "/",
			MaxAge: int(stateTTL.Seconds()), HttpOnly: true,
			Secure: secure, SameSite: http.SameSiteLaxMode,
		})
		sum := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(sum[:])
		dst := authURL +
			"&state=" + url.QueryEscape(state) +
			"&code_challenge=" + url.QueryEscape(challenge) +
			"&code_challenge_method=S256"
		http.Redirect(w, r, dst, http.StatusTemporaryRedirect)
	}
}

func currentSub(s *store.Store, secret []byte, r *http.Request) string {
	if hdr := r.Header.Get("Authorization"); strings.HasPrefix(hdr, "Bearer ") {
		if c, err := VerifyJWT(secret, strings.TrimPrefix(hdr, "Bearer ")); err == nil {
			return c.Sub
		}
	}
	if s == nil {
		return ""
	}
	cookie, err := r.Cookie(cookieName)
	if err != nil {
		return ""
	}
	sess, ok := s.AuthSession(HashToken(cookie.Value))
	if !ok || time.Now().After(sess.ExpiresAt) {
		return ""
	}
	return s.CanonicalSub(sess.UserSub)
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

func handleGitHubRedirect(cfg *core.Config, s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	cb := authBaseURL(cfg) + "/auth/github/callback"
	u := fmt.Sprintf(
		"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&scope=read:user",
		url.QueryEscape(cfg.GitHubClientID), url.QueryEscape(cb))
	return oauthRedirect(s, secret, secure, u)
}

func oauthCallbackCode(secret []byte, w http.ResponseWriter, r *http.Request, secure bool) (code, verifier string, intent stateIntent, ok bool) {
	intent, ok = verifyState(secret, r)
	if !ok {
		http.Error(w, "invalid state", http.StatusForbidden)
		return "", "", intent, false
	}
	code = r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return "", "", intent, false
	}
	verifier = consumePKCE(w, r, secure)
	return code, verifier, intent, true
}

func handleGitHubCallback(cfg *core.Config, s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, verifier, intent, ok := oauthCallbackCode(secret, w, r, secure)
		if !ok {
			return
		}
		token, err := exchangeGitHub(r.Context(), cfg, code, verifier)
		if err != nil {
			slog.Error("github token exchange failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		sub, name, err := fetchGitHubUser(r.Context(), token)
		if err != nil {
			slog.Error("github user fetch failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		if org := cfg.GitHubAllowedOrg; org != "" {
			if !checkGitHubOrgMember(r.Context(), token, org, sub) {
				http.Redirect(w, r, "/auth/login?error=unauthorized", http.StatusTemporaryRedirect)
				return
			}
		}
		dispatchOAuth(w, r, s, secret, "github:"+sub, name, intent, secure)
	}
}

func handleDiscordRedirect(cfg *core.Config, s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	cb := authBaseURL(cfg) + "/auth/discord/callback"
	u := fmt.Sprintf(
		"https://discord.com/api/oauth2/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=identify",
		url.QueryEscape(cfg.DiscordClientID), url.QueryEscape(cb))
	return oauthRedirect(s, secret, secure, u)
}

func handleDiscordCallback(cfg *core.Config, s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, verifier, intent, ok := oauthCallbackCode(secret, w, r, secure)
		if !ok {
			return
		}
		token, err := exchangeDiscord(r.Context(), cfg, code, verifier)
		if err != nil {
			slog.Error("discord token exchange failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		sub, name, err := fetchDiscordUser(r.Context(), token)
		if err != nil {
			slog.Error("discord user fetch failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		dispatchOAuth(w, r, s, secret, "discord:"+sub, name, intent, secure)
	}
}

func handleGoogleRedirect(cfg *core.Config, s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	cb := authBaseURL(cfg) + "/auth/google/callback"
	hd := ""
	if cfg.GoogleAllowedEmails != "" {
		seen := map[string]struct{}{}
		for _, p := range strings.Split(cfg.GoogleAllowedEmails, ",") {
			p = strings.TrimSpace(p)
			if i := strings.Index(p, "@"); i >= 0 {
				d := strings.TrimPrefix(p[i+1:], "*.")
				if d != "" {
					seen[d] = struct{}{}
				}
			}
		}
		if len(seen) == 1 {
			for d := range seen {
				hd = "&hd=" + url.QueryEscape(d)
			}
		}
	}
	u := fmt.Sprintf(
		"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&response_type=code&scope=openid%%20email%%20profile%s",
		url.QueryEscape(cfg.GoogleClientID), url.QueryEscape(cb), hd)
	return oauthRedirect(s, secret, secure, u)
}

func handleGoogleCallback(cfg *core.Config, s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		code, verifier, intent, ok := oauthCallbackCode(secret, w, r, secure)
		if !ok {
			return
		}
		token, err := exchangeGoogle(r.Context(), cfg, code, verifier)
		if err != nil {
			slog.Error("google token exchange failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		sub, name, email, verified, err := fetchGoogleUser(r.Context(), token)
		if err != nil {
			slog.Error("google user fetch failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		if allowed := cfg.GoogleAllowedEmails; allowed != "" {
			if !verified || !matchEmailAllowlist(email, allowed) {
				http.Redirect(w, r, "/auth/login?error=unauthorized", http.StatusTemporaryRedirect)
				return
			}
		}
		dispatchOAuth(w, r, s, secret, "google:"+sub, name, intent, secure)
	}
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

func handleTelegram(cfg *core.Config, s *store.Store, secret []byte, secure bool) http.HandlerFunc {
	botToken := cfg.TelegramToken
	return func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !verifyTelegramWidget(r.Form, botToken) {
			http.Error(w, "invalid telegram auth", http.StatusForbidden)
			return
		}
		sub := r.FormValue("id")
		name := r.FormValue("first_name")
		if ln := r.FormValue("last_name"); ln != "" {
			name += " " + ln
		}
		dispatchOAuth(w, r, s, secret, "telegram:"+sub, name, stateIntent{}, secure)
	}
}

func dispatchOAuth(w http.ResponseWriter, r *http.Request, s *store.Store, secret []byte, sub, name string, intent stateIntent, secure bool) {
	if i := strings.Index(sub, ":"); i < 0 || i == len(sub)-1 {
		slog.Error("oauth empty identity", "sub", sub)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}

	existing, exists := s.AuthUserBySub(sub)
	subCanonical := ""
	if exists {
		if existing.LinkedToSub != "" {
			subCanonical = existing.LinkedToSub
		} else {
			subCanonical = sub
		}
	}

	sessionSub := currentSub(s, secret, r)
	linking := intent.Intent == "link" && intent.LinkFrom != ""

	switch {
	case linking && exists && existing.LinkedToSub == intent.LinkFrom:
		issueSession(w, r, s, secret, intent.LinkFrom, name, secure)

	case linking && exists && subCanonical != intent.LinkFrom:
		renderCollision(w, secret, sub, name, subCanonical, intent.LinkFrom, secure)

	case linking && !exists:
		if err := s.LinkSubToCanonical(sub, name, intent.LinkFrom); err != nil {
			slog.Error("link sub to canonical", "sub", sub, "canonical", intent.LinkFrom, "err", err)
			http.Error(w, "link failed", http.StatusBadGateway)
			return
		}
		issueSession(w, r, s, secret, intent.LinkFrom, name, secure)

	case !linking && sessionSub != "" && !exists:
		renderCollision(w, secret, sub, name, "", sessionSub, secure)

	case !linking && sessionSub != "" && exists && subCanonical != sessionSub:
		renderCollision(w, secret, sub, name, subCanonical, sessionSub, secure)

	case !linking && sessionSub == "" && !exists:
		if err := s.CreateAuthUser(sub, sub, "", name); err != nil {
			slog.Error("create oauth user failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		issueSession(w, r, s, secret, sub, name, secure)

	default:
		issueSession(w, r, s, secret, sub, name, secure)
	}
}

type stateIntent struct {
	Intent   string `json:"i,omitempty"`
	LinkFrom string `json:"f,omitempty"`
	Return   string `json:"r,omitempty"`
}

func signState(secret []byte) string {
	return signStateP(secret, stateIntent{})
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
