package main

// OAuth / login surface (/auth/*), owned by authd (spec 5/1 § OAuth routes).
// authd is the OAuth provider: the provider dance (Google/GitHub/Discord/
// Telegram) is reused from the auth library's pure wrappers; only issuance
// differs — authd mints an ES256 access token (~15m) and a rotating refresh
// in its own refresh_tokens store (NOT messages.db store.AuthSession). proxyd
// no longer mounts any /auth/* routes; it 302s unauthenticated callers here.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"html/template"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
	"github.com/kronael/arizuko/theme"
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
	errMsg := r.URL.Query().Get("error")
	if errMsg == "unauthorized" {
		b.WriteString(`<p class="banner-err">Not authorised — contact the operator to request access.</p>`)
	}
	b.WriteString(`<p class="sub">Sign in to continue</p>`)
	if o.cfg.GoogleClientID != "" {
		b.WriteString(`<a class="oauth-btn" href="/auth/google">Sign in with Google</a>`)
	}
	if o.cfg.GitHubClientID != "" {
		b.WriteString(`<a class="oauth-btn" href="/auth/github">Sign in with GitHub</a>`)
	}
	if o.cfg.DiscordClientID != "" {
		b.WriteString(`<a class="oauth-btn" href="/auth/discord">Sign in with Discord</a>`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, theme.Page("Sign in", template.HTML(b.String())))
}

// redirect builds the provider authorize URL and writes the CSRF state + PKCE
// cookies, then 302s. ?intent=link carries the current sub for account linking.
//
// The return path rides ONE carrier: the signed StateIntent.Return, sourced from
// ?return= (link flow) or the auth_return cookie proxyd's requireAuth drops when
// it bounces an unauthenticated caller here. Reading the cookie is what makes
// "land back on the page you asked for" true — it was written and never read
// before, so a bounced caller landed on / instead (spec 5/31 needs the pairing
// URL to survive the round-trip). The cookie is validated by SafeReturn before
// it enters the SIGNED state and cleared on consume so it cannot leak into a
// later, unrelated login.
func (o *oauth) redirect(provider string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ret := o.consumeReturn(w, r)
		intent := auth.StateIntent{Return: ret}
		if r.URL.Query().Get("intent") == "link" {
			if from := o.bearerSub(r); from != "" {
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

// authReturnCookie is the same-origin hand-off proxyd (and onbod) write before
// bouncing a caller to /auth/login.
const authReturnCookie = "auth_return"

// consumeReturn reads the post-login destination — ?return= wins, else the
// auth_return cookie — validates it as a local path and clears the cookie.
// Returns "" when there is no safe destination.
func (o *oauth) consumeReturn(w http.ResponseWriter, r *http.Request) string {
	raw := r.URL.Query().Get("return")
	if raw == "" {
		if c, err := r.Cookie(authReturnCookie); err == nil {
			raw = c.Value
		}
	}
	if raw == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name: authReturnCookie, Value: "", Path: "/",
		MaxAge: -1, HttpOnly: true, Secure: o.secure, SameSite: http.SameSiteLaxMode,
	})
	safe, ok := auth.SafeReturn(raw)
	if !ok {
		slog.Warn("discarding unsafe post-login return path", "return", raw)
		return ""
	}
	return safe
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
// identify the external identity; the account's canonical sub is
// auth_users.user_id.
//
// **Alias resolution happens here, at mint** (spec 5/32 § Alias resolution).
// `acl.principal` keys on provider subs, so a returning login that is a LINKED
// ALIAS must mint the ACCOUNT's canonical sub or it is a principal with none of
// the account's grants. Nothing downstream resolves: auth.Authorize evaluates
// the subject it is handed.
//
// The three cases of spec 5/1 § Account linking, in order:
//   - explicit link — attach to the caller's own (already canonical) account;
//   - returning login — resolve the identity to the account that owns it;
//   - first login — the identity is its own canonical, and upsert creates it.
func (o *oauth) dispatch(w http.ResponseWriter, r *http.Request, provider, providerSub, name string, intent auth.StateIntent) {
	if providerSub == "" {
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	login := provider + ":" + providerSub
	canonical := login
	if intent.Intent == "link" && intent.LinkFrom != "" {
		canonical = intent.LinkFrom
	} else if owner, _, _, _, linked := oauthIdentityForSub(o.a.db, login); linked {
		canonical = owner
	}
	if err := upsertOAuthUser(o.a.db, canonical, name, provider, providerSub); err != nil {
		// UNIQUE(provider, provider_sub) fires when a link targets an identity
		// another account already owns — the hard fail of spec 5/1, not a merge.
		slog.Error("oauth upsert user", "user", canonical, "login", login, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	o.issueSession(w, r, canonical, login, intent.Return)
}

// issueSession is the ES256 counterpart of auth/web.go issueSession: snapshot
// scope from grants, mint the access JWT, create a refresh_tokens row, deliver
// per Accept (browser → HttpOnly cookie + localStorage bootstrap; JSON →
// {token,expires_at,refresh_token}).
//
// sub is the account's canonical sub; login is the provider identity actually
// presented. They differ whenever a linked alias signs in.
func (o *oauth) issueSession(w http.ResponseWriter, r *http.Request, sub, login, returnTo string) {
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

	// The subject is the ACCOUNT's canonical sub, so from here on nothing can
	// tell which provider login was presented — not the JWT, not proxyd's
	// X-User-Sub, not the audit actor derived from it. Record it at the one
	// point that still knows both. Not a JWT claim: refresh_tokens stores only
	// the canonical sub, so a login claim would vanish on the first refresh and
	// a half-present claim is worse than none.
	audit.Emit(r.Context(), audit.Event{
		Category: audit.CategoryAuthN,
		Action:   "login",
		Actor:    claimSub,
		ActorSub: sub,
		Resource: login,
		Scope:    strings.Join(scope, ","),
		Surface:  audit.SurfaceGateway,
		Folder:   folder,
		Outcome:  audit.OutcomeOK,
	})

	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, map[string]any{"token": access, "expires_at": exp, "refresh_token": refresh})
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: "refresh_token", Value: refresh, Path: "/",
		Expires: time.Now().Add(o.a.refreshTTL), HttpOnly: true,
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

// bearerSub returns the verified BARE sub of the request's bearer ("" if
// none/invalid). Bare, not the raw claim: its one consumer is StateIntent.
// LinkFrom, which lands in auth_users.user_id, and that column stores the sub
// without the "user:" prefix (spec 5/1 § "sub prefix rule"). Returning the raw
// claim forked the account instead of linking to it — the link wrote a second
// auth_users row keyed "user:google:alice" and the next mint double-prefixed it
// to "user:user:google:alice".
func (o *oauth) bearerSub(r *http.Request) string {
	if sub, err := auth.VerifyHTTP(r, o.a.LocalKeySet()); err == nil {
		return bareSub(sub.Sub)
	}
	return ""
}
