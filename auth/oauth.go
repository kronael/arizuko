package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/onvos/arizuko/core"
	"github.com/onvos/arizuko/store"
)

const stateTTL = 10 * time.Minute

var googleTokenURL    = "https://oauth2.googleapis.com/token"
var googleUserinfoURL = "https://www.googleapis.com/oauth2/v3/userinfo"

func handleGitHubRedirect(cfg *core.Config, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := signState(secret)
		http.SetCookie(w, &http.Cookie{
			Name: "oauth_state", Value: state, Path: "/",
			MaxAge: int(stateTTL.Seconds()), HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		cb := authBaseURL(cfg) + "/auth/github/callback"
		u := fmt.Sprintf(
			"https://github.com/login/oauth/authorize?client_id=%s&redirect_uri=%s&state=%s&scope=read:user",
			url.QueryEscape(cfg.GitHubClientID),
			url.QueryEscape(cb),
			url.QueryEscape(state),
		)
		http.Redirect(w, r, u, http.StatusTemporaryRedirect)
	}
}

func handleGitHubCallback(cfg *core.Config, s *store.Store, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyState(secret, r) {
			http.Error(w, "invalid state", http.StatusForbidden)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		token, err := exchangeGitHub(cfg, code)
		if err != nil {
			slog.Error("github token exchange failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		sub, name, err := fetchGitHubUser(token)
		if err != nil {
			slog.Error("github user fetch failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		if org := cfg.GitHubAllowedOrg; org != "" {
			if !checkGitHubOrgMember(token, org, sub) {
				http.Redirect(w, r, "/auth/login?error=unauthorized", http.StatusTemporaryRedirect)
				return
			}
		}
		createOAuthSession(w, s, secret, "github:"+sub, name)
	}
}

func handleDiscordRedirect(cfg *core.Config, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := signState(secret)
		http.SetCookie(w, &http.Cookie{
			Name: "oauth_state", Value: state, Path: "/",
			MaxAge: int(stateTTL.Seconds()), HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
		cb := authBaseURL(cfg) + "/auth/discord/callback"
		u := fmt.Sprintf(
			"https://discord.com/api/oauth2/authorize?client_id=%s&redirect_uri=%s&state=%s&response_type=code&scope=identify",
			url.QueryEscape(cfg.DiscordClientID),
			url.QueryEscape(cb),
			url.QueryEscape(state),
		)
		http.Redirect(w, r, u, http.StatusTemporaryRedirect)
	}
}

func handleDiscordCallback(cfg *core.Config, s *store.Store, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyState(secret, r) {
			http.Error(w, "invalid state", http.StatusForbidden)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		token, err := exchangeDiscord(cfg, code)
		if err != nil {
			slog.Error("discord token exchange failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		sub, name, err := fetchDiscordUser(token)
		if err != nil {
			slog.Error("discord user fetch failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		createOAuthSession(w, s, secret, "discord:"+sub, name)
	}
}

func handleGoogleRedirect(cfg *core.Config, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		state := signState(secret)
		http.SetCookie(w, &http.Cookie{
			Name: "oauth_state", Value: state, Path: "/",
			MaxAge: int(stateTTL.Seconds()), HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		cb := authBaseURL(cfg) + "/auth/google/callback"
		u := fmt.Sprintf(
			"https://accounts.google.com/o/oauth2/v2/auth?client_id=%s&redirect_uri=%s&response_type=code&scope=openid%%20email%%20profile&state=%s%s",
			url.QueryEscape(cfg.GoogleClientID),
			url.QueryEscape(cb),
			url.QueryEscape(state),
			googleWorkspaceHD(cfg.GoogleAllowedEmails),
		)
		http.Redirect(w, r, u, http.StatusTemporaryRedirect)
	}
}

// googleWorkspaceHD returns the &hd= hint for Google OAuth when all allowed
// email patterns share a single domain (e.g. "*@example.com"). This restricts
// the sign-in picker to that workspace at Google's level, before our callback.
func googleWorkspaceHD(allowedEmails string) string {
	if allowedEmails == "" {
		return ""
	}
	seen := map[string]struct{}{}
	for _, p := range strings.Split(allowedEmails, ",") {
		p = strings.TrimSpace(p)
		if i := strings.Index(p, "@"); i >= 0 {
			domain := p[i+1:]
			domain = strings.TrimPrefix(domain, "*.")
			if domain != "" {
				seen[domain] = struct{}{}
			}
		}
	}
	if len(seen) == 1 {
		for d := range seen {
			return "&hd=" + url.QueryEscape(d)
		}
	}
	return ""
}

func handleGoogleCallback(cfg *core.Config, s *store.Store, secret []byte) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !verifyState(secret, r) {
			http.Error(w, "invalid state", http.StatusForbidden)
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		token, err := exchangeGoogle(cfg, code)
		if err != nil {
			slog.Error("google token exchange failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		sub, name, email, err := fetchGoogleUser(token)
		if err != nil {
			slog.Error("google user fetch failed", "err", err)
			http.Error(w, "oauth failed", http.StatusBadGateway)
			return
		}
		if allowed := cfg.GoogleAllowedEmails; allowed != "" {
			if !matchEmailAllowlist(email, allowed) {
				http.Redirect(w, r, "/auth/login?error=unauthorized", http.StatusTemporaryRedirect)
				return
			}
		}
		createOAuthSession(w, s, secret, "google:"+sub, name)
	}
}

func exchangeGoogle(cfg *core.Config, code string) (string, error) {
	cb := authBaseURL(cfg) + "/auth/google/callback"
	resp, err := http.PostForm(googleTokenURL, url.Values{
		"code":          {code},
		"client_id":     {cfg.GoogleClientID},
		"client_secret": {cfg.GoogleSecret},
		"redirect_uri":  {cb},
		"grant_type":    {"authorization_code"},
	})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", err
	}
	if tok.AccessToken == "" {
		return "", fmt.Errorf("empty access token")
	}
	return tok.AccessToken, nil
}

func fetchGoogleUser(token string) (sub, name, email string, err error) {
	req, _ := http.NewRequest("GET", googleUserinfoURL, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	var u struct {
		Sub   string `json:"sub"`
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", "", "", err
	}
	return u.Sub, u.Name, u.Email, nil
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

func checkGitHubOrgMember(token, org, username string) bool {
	u := fmt.Sprintf("https://api.github.com/orgs/%s/members/%s",
		url.PathEscape(org), url.PathEscape(username))
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("github org check failed", "org", org, "err", err)
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusNoContent
}

func handleTelegram(cfg *core.Config, s *store.Store, secret []byte) http.HandlerFunc {
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
		createOAuthSession(w, s, secret, "telegram:"+sub, name)
	}
}

func createOAuthSession(w http.ResponseWriter, s *store.Store, secret []byte, sub, name string) {
	if _, ok := s.AuthUserBySub(sub); !ok {
		username := sub
		if err := s.CreateAuthUser(sub, username, "", name); err != nil {
			slog.Error("create oauth user failed", "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
	}
	issueSession(w, s, secret, sub, name)
}

// state cookie: timestamp.hmac(secret, timestamp)
func signState(secret []byte) string {
	ts := fmt.Sprintf("%d", time.Now().Unix())
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(ts))
	sig := hex.EncodeToString(mac.Sum(nil))
	return ts + "." + sig
}

func verifyState(secret []byte, r *http.Request) bool {
	cookie, err := r.Cookie("oauth_state")
	if err != nil {
		return false
	}
	state := r.URL.Query().Get("state")
	if state != cookie.Value {
		return false
	}
	parts := strings.SplitN(state, ".", 2)
	if len(parts) != 2 {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0]))
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(parts[1]), []byte(expected)) {
		return false
	}
	var ts int64
	fmt.Sscanf(parts[0], "%d", &ts)
	return time.Since(time.Unix(ts, 0)) < stateTTL
}

func verifyTelegramWidget(form url.Values, botToken string) bool {
	hash := form.Get("hash")
	if hash == "" {
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
	if cfg.AuthBaseURL != "" {
		return strings.TrimRight(cfg.AuthBaseURL, "/")
	}
	if cfg.WebHost != "" {
		return "https://" + cfg.WebHost
	}
	return ""
}

func exchangeGitHub(cfg *core.Config, code string) (string, error) {
	data := url.Values{
		"client_id":     {cfg.GitHubClientID},
		"client_secret": {cfg.GitHubSecret},
		"code":          {code},
	}
	req, _ := http.NewRequest("POST",
		"https://github.com/login/oauth/access_token",
		strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var result struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Error != "" {
		return "", fmt.Errorf("github: %s", result.Error)
	}
	return result.AccessToken, nil
}

func fetchGitHubUser(token string) (string, string, error) {
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var u struct {
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	json.NewDecoder(resp.Body).Decode(&u)
	name := u.Name
	if name == "" {
		name = u.Login
	}
	return u.Login, name, nil
}

func exchangeDiscord(cfg *core.Config, code string) (string, error) {
	cb := authBaseURL(cfg) + "/auth/discord/callback"
	data := url.Values{
		"client_id":     {cfg.DiscordClientID},
		"client_secret": {cfg.DiscordSecret},
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {cb},
	}
	resp, err := http.PostForm(
		"https://discord.com/api/oauth2/token", data)
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
	json.NewDecoder(resp.Body).Decode(&result)
	return result.AccessToken, nil
}

func fetchDiscordUser(token string) (string, string, error) {
	req, _ := http.NewRequest("GET",
		"https://discord.com/api/users/@me", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	var u struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Global   string `json:"global_name"`
	}
	json.NewDecoder(resp.Body).Decode(&u)
	name := u.Global
	if name == "" {
		name = u.Username
	}
	return u.ID, name, nil
}
