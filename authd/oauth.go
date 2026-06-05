package main

// OAuth / login surface (/auth/*), ported into authd from auth/web.go +
// auth/oauth.go + auth/routes.go (spec 5/1 § OAuth routes). authd is now the
// OAuth provider: the provider dance (Google/GitHub/Discord/Telegram) is reused
// verbatim from the auth library; only issuance changes — authd mints an ES256
// access token (~15m) and a rotating refresh in its own refresh_tokens store
// (NOT messages.db store.AuthSession). The dual-verify soak keeps proxyd's
// HS256 RegisterRoutes path alive in parallel; this is additive.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// oauth carries everything the /auth/* handlers need: the signer (authd), the
// provider config, the CSRF-state HMAC key, and the optional grants fetcher for
// the login-time scope snapshot.
type oauth struct {
	a      *Authd
	cfg    *core.Config
	state  []byte // CSRF state HMAC key (a CSRF token, not identity — stays symmetric)
	secure bool
	grants GrantsFetcher
}

// registerOAuth mounts /auth/* on mux, conditionally per configured provider
// (spec 5/1 § OAuth routes). Mounted only when AUTH_BASE_URL is set (the
// callback URL authd registers with each IdP).
func (s *server) registerOAuth(mux *http.ServeMux, cfg *core.Config) {
	if cfg == nil || cfg.AuthBaseURL == "" {
		return
	}
	o := &oauth{
		a:      s.a,
		cfg:    cfg,
		state:  []byte(cfg.AuthSecret),
		secure: strings.HasPrefix(auth.AuthBaseURL(cfg), "https://"),
		grants: s.grants,
	}
	mux.HandleFunc("GET /auth/login", o.loginPage)
	mux.HandleFunc("POST /auth/logout", o.logout)
	mux.HandleFunc("GET /auth/me", o.me)

	if cfg.GoogleClientID != "" {
		mux.HandleFunc("GET /auth/google", o.redirect("google"))
		mux.HandleFunc("GET /auth/google/callback", o.googleCallback)
	}
	if cfg.GitHubClientID != "" {
		mux.HandleFunc("GET /auth/github", o.redirect("github"))
		mux.HandleFunc("GET /auth/github/callback", o.githubCallback)
	}
	if cfg.DiscordClientID != "" {
		mux.HandleFunc("GET /auth/discord", o.redirect("discord"))
		mux.HandleFunc("GET /auth/discord/callback", o.discordCallback)
	}
	if cfg.TelegramToken != "" {
		mux.HandleFunc("POST /auth/telegram", o.telegram)
	}
}

func (o *oauth) loginPage(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder
	if o.cfg.GoogleClientID != "" {
		b.WriteString(`<a href="/auth/google">Sign in with Google</a> `)
	}
	if o.cfg.GitHubClientID != "" {
		b.WriteString(`<a href="/auth/github">Sign in with GitHub</a> `)
	}
	if o.cfg.DiscordClientID != "" {
		b.WriteString(`<a href="/auth/discord">Sign in with Discord</a>`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><body><h1>arizuko</h1>%s</body></html>`, b.String())
}

// redirect builds the provider authorize URL and writes the CSRF state + PKCE
// cookies, then 302s. ?intent=link carries the current sub for account linking.
func (o *oauth) redirect(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		intent := auth.StateIntent{}
		if r.URL.Query().Get("intent") == "link" {
			if from := o.bearerSub(r); from != "" {
				ret := ""
				if rt := r.URL.Query().Get("return"); rt != "" {
					if safe, ok := auth.SafeReturn(rt); ok {
						ret = safe
					}
				}
				intent = auth.NewLinkIntent(from, ret)
			}
		}
		state := auth.SignState(o.state, intent)
		auth.WriteStateCookie(w, state, o.secure)
		challenge, err := auth.WritePKCE(w, o.secure)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		dst := o.authorizeURL(provider) +
			"&state=" + url.QueryEscape(state) +
			"&code_challenge=" + url.QueryEscape(challenge) +
			"&code_challenge_method=S256"
		http.Redirect(w, r, dst, http.StatusTemporaryRedirect)
	}
}

func (o *oauth) authorizeURL(provider string) string {
	cb := auth.AuthBaseURL(o.cfg) + "/auth/" + provider + "/callback"
	switch provider {
	case "google":
		return "https://accounts.google.com/o/oauth2/v2/auth?client_id=" +
			url.QueryEscape(o.cfg.GoogleClientID) + "&redirect_uri=" + url.QueryEscape(cb) +
			"&response_type=code&scope=openid%20email%20profile"
	case "github":
		return "https://github.com/login/oauth/authorize?client_id=" +
			url.QueryEscape(o.cfg.GitHubClientID) + "&redirect_uri=" + url.QueryEscape(cb) +
			"&scope=read:user"
	case "discord":
		return "https://discord.com/api/oauth2/authorize?client_id=" +
			url.QueryEscape(o.cfg.DiscordClientID) + "&redirect_uri=" + url.QueryEscape(cb) +
			"&response_type=code&scope=identify"
	}
	return ""
}

// callbackCode validates the CSRF state and pulls the code + PKCE verifier
// (shared callback prologue across providers).
func (o *oauth) callbackCode(w http.ResponseWriter, r *http.Request) (code, verifier string, intent auth.StateIntent, ok bool) {
	intent, ok = auth.VerifyState(o.state, r)
	if !ok {
		http.Error(w, "invalid state", http.StatusForbidden)
		return "", "", intent, false
	}
	code = r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return "", "", intent, false
	}
	return code, auth.ConsumePKCE(w, r, o.secure), intent, true
}

func (o *oauth) googleCallback(w http.ResponseWriter, r *http.Request) {
	code, verifier, intent, ok := o.callbackCode(w, r)
	if !ok {
		return
	}
	tok, err := auth.ExchangeGoogle(r.Context(), o.cfg, code, verifier)
	if err != nil {
		slog.Error("google token exchange", "err", err)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	sub, name, email, verified, err := auth.FetchGoogleUser(r.Context(), tok)
	if err != nil {
		slog.Error("google user fetch", "err", err)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	if allowed := o.cfg.GoogleAllowedEmails; allowed != "" {
		if !verified || !auth.MatchEmailAllowlist(email, allowed) {
			http.Redirect(w, r, "/auth/login?error=unauthorized", http.StatusTemporaryRedirect)
			return
		}
	}
	o.dispatch(w, r, "google", sub, name, intent)
}

func (o *oauth) githubCallback(w http.ResponseWriter, r *http.Request) {
	code, verifier, intent, ok := o.callbackCode(w, r)
	if !ok {
		return
	}
	tok, err := auth.ExchangeGitHub(r.Context(), o.cfg, code, verifier)
	if err != nil {
		slog.Error("github token exchange", "err", err)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	sub, name, err := auth.FetchGitHubUser(r.Context(), tok)
	if err != nil {
		slog.Error("github user fetch", "err", err)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	if org := o.cfg.GitHubAllowedOrg; org != "" && !auth.CheckGitHubOrgMember(r.Context(), tok, org, sub) {
		http.Redirect(w, r, "/auth/login?error=unauthorized", http.StatusTemporaryRedirect)
		return
	}
	o.dispatch(w, r, "github", sub, name, intent)
}

func (o *oauth) discordCallback(w http.ResponseWriter, r *http.Request) {
	code, verifier, intent, ok := o.callbackCode(w, r)
	if !ok {
		return
	}
	tok, err := auth.ExchangeDiscord(r.Context(), o.cfg, code, verifier)
	if err != nil {
		slog.Error("discord token exchange", "err", err)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	sub, name, err := auth.FetchDiscordUser(r.Context(), tok)
	if err != nil {
		slog.Error("discord user fetch", "err", err)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	o.dispatch(w, r, "discord", sub, name, intent)
}

func (o *oauth) telegram(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if !auth.VerifyTelegramWidget(r.Form, o.cfg.TelegramToken) {
		http.Error(w, "invalid telegram auth", http.StatusForbidden)
		return
	}
	name := r.FormValue("first_name")
	if ln := r.FormValue("last_name"); ln != "" {
		name += " " + ln
	}
	o.dispatch(w, r, "telegram", r.FormValue("id"), name, auth.StateIntent{})
}

// dispatch resolves the provider identity to a canonical authd user (creating
// or linking as needed) and issues an ES256 session. provider+providerSub
// identify the external identity; the canonical user_id is "<provider>:<sub>"
// for a first login (the simple-stays-simple default — the full collision UI is
// a later step, spec 5/1 § Account linking).
func (o *oauth) dispatch(w http.ResponseWriter, r *http.Request, provider, providerSub, name string, intent auth.StateIntent) {
	if providerSub == "" {
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	userID := provider + ":" + providerSub
	canonical := userID
	if intent.Intent == "link" && intent.LinkFrom != "" {
		canonical = intent.LinkFrom
	}
	if err := upsertOAuthUser(o.a.db, canonical, name, provider, providerSub); err != nil {
		slog.Error("oauth upsert user", "user", canonical, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	o.issueSession(w, r, canonical, intent.Return)
}

// issueSession is the ES256 counterpart of auth/web.go issueSession: snapshot
// scope from grants, mint the access JWT, create a refresh_tokens row, deliver
// per Accept (browser → HttpOnly cookie + localStorage bootstrap; JSON →
// {token,expires_at,refresh_token}).
func (o *oauth) issueSession(w http.ResponseWriter, r *http.Request, sub, returnTo string) {
	scope, folder, ok := o.snapshot(r.Context(), sub)
	if !ok {
		writeErr(w, http.StatusServiceUnavailable, "grants_unavailable", "grants backend unavailable")
		return
	}
	claimSub := "user:" + sub
	// One mint path, mirroring IssuerMint: signMinted stamps a single jti, the
	// arz/folder claim, and typ="user" in one Sign — no mint-then-discard.
	m, err := o.a.signMinted(auth.TokenClaims{Sub: claimSub, Typ: "user", Scope: scope}, folder, o.a.accessTTL)
	if err != nil {
		slog.Error("mint access", "sub", sub, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	access := m.token
	// Store the BARE canonical sub in refresh_tokens (spec 5/1 § JWT claim set
	// "sub prefix rule": the user:/service: prefix lives ONLY in the JWT sub
	// claim, never in DB columns). Refresh re-adds the prefix when it mints.
	refresh, err := o.a.IssueRefresh(sub, scope, "")
	if err != nil {
		slog.Error("issue refresh", "sub", sub, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	exp := time.Now().Add(o.a.accessTTL).UTC().Format(time.RFC3339)

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, map[string]any{"token": access, "expires_at": exp, "refresh_token": refresh})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "refresh_token", Value: refresh, Path: "/",
		Expires: time.Now().Add(refreshTTL), HttpOnly: true,
		Secure: o.secure, SameSite: http.SameSiteLaxMode,
	})
	dest := "/"
	if len(scope) == 0 {
		dest = "/onboard"
	}
	if safe, ok := auth.SafeReturn(returnTo); ok {
		dest = safe
	}
	accessJS, _ := json.Marshal(access)
	destJS, _ := json.Marshal(dest)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!DOCTYPE html><html><head><script>localStorage.setItem('jwt',%s);window.location=%s;</script></head><body></body></html>`,
		auth.JSSafe(accessJS), auth.JSSafe(destJS))
}

// snapshot resolves the login-time scope ceiling (spec 5/1 § Login-time scope
// snapshot): empty-scope when no grants fetcher is wired or the sub has no
// grants (authenticated-but-unauthorized → /onboard); fail-closed (ok=false)
// only when the grants backend is down.
func (o *oauth) snapshot(ctx context.Context, bareSub string) (scope []string, folder string, ok bool) {
	if o.grants == nil {
		return nil, "", true
	}
	snap, err := o.grants.FetchGrants(ctx, bareSub)
	if err != nil {
		if err == ErrNoGrants {
			return nil, "", true
		}
		return nil, "", false
	}
	return snap.Scope, snap.Folder, true
}

// logout revokes the refresh token's family and clears the cookie.
func (o *oauth) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("refresh_token"); err == nil && c.Value != "" {
		if row, found := lookupRefresh(o.a.db, c.Value); found {
			_ = revokeFamily(o.a.db, row.family)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: "refresh_token", Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
}

// me returns the caller's own verified identity (bearer access JWT).
func (o *oauth) me(w http.ResponseWriter, r *http.Request) {
	sub, err := auth.VerifyHTTP(r, o.a.LocalKeySet())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized", "valid bearer required")
		return
	}
	writeJSON(w, map[string]any{
		"sub":        sub.Sub,
		"scope":      sub.Scope,
		"folder":     sub.Extra["arz/folder"],
		"expires_at": sub.Expires.UTC().Format(time.RFC3339),
	})
}

// bearerSub returns the verified sub of the request's bearer ("" if none/invalid).
func (o *oauth) bearerSub(r *http.Request) string {
	if sub, err := auth.VerifyHTTP(r, o.a.LocalKeySet()); err == nil {
		return sub.Sub
	}
	return ""
}
