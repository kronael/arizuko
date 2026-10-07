package main

// Dex login, spec 6/22 step 4: Dex authenticates, authd maps the ID token to a provider principal.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"

	"github.com/kronael/arizuko/auth"
	"github.com/kronael/arizuko/core"
)

// federated:id puts the upstream connector id + user id on the ID token.
const dexScope = "openid profile federated:id"

var dexHTTP = &http.Client{Timeout: 15 * time.Second}

func dexConfigured(cfg *core.Config) bool {
	return cfg.DexIssuer != "" && cfg.DexClientID != ""
}

// Discovery on first use: authd boots with Dex down (no depends_on).
func (o *oauth) dexProvider() (*oidc.Provider, error) {
	o.dexMu.Lock()
	defer o.dexMu.Unlock()
	if o.dex != nil {
		return o.dex, nil
	}
	// The key set refetches JWKS for authd's lifetime, not a request's.
	p, err := oidc.NewProvider(oidc.ClientContext(context.Background(), dexHTTP), o.cfg.DexIssuer)
	if err != nil {
		return nil, fmt.Errorf("dex discovery: %w", err)
	}
	o.dex = p
	return p, nil
}

func (o *oauth) dexAuthorizeURL(callback string) (string, error) {
	p, err := o.dexProvider()
	if err != nil {
		return "", err
	}
	return p.Endpoint().AuthURL + "?client_id=" + url.QueryEscape(o.cfg.DexClientID) +
		"&redirect_uri=" + url.QueryEscape(callback) +
		"&response_type=code&scope=" + url.QueryEscape(dexScope), nil
}

func (o *oauth) dexCallback(w http.ResponseWriter, r *http.Request) {
	code, verifier, intent, ok := o.callbackCode(w, r)
	if !ok {
		return
	}
	provider, providerSub, name, err := o.dexIdentity(r.Context(), code, verifier)
	if err != nil {
		slog.Error("dex login", "err", err)
		http.Error(w, "oauth failed", http.StatusBadGateway)
		return
	}
	o.dispatch(w, r, provider, providerSub, name, intent)
}

// The connector id is the principal namespace: "discord" mints "discord:<id>" like the native callback.
func (o *oauth) dexIdentity(ctx context.Context, code, verifier string) (provider, providerSub, name string, err error) {
	p, err := o.dexProvider()
	if err != nil {
		return "", "", "", err
	}
	data := url.Values{
		"grant_type":   {"authorization_code"},
		"code":         {code},
		"redirect_uri": {auth.AuthBaseURL(o.cfg) + "/auth/dex/callback"},
		"client_id":    {o.cfg.DexClientID},
	}
	if o.cfg.DexSecret != "" {
		data.Set("client_secret", o.cfg.DexSecret)
	}
	if verifier != "" {
		data.Set("code_verifier", verifier)
	}
	resp, err := auth.PostForm(ctx, p.Endpoint().TokenURL, data, "application/json")
	if err != nil {
		return "", "", "", fmt.Errorf("dex token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tok struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil {
		return "", "", "", fmt.Errorf("dex token: decode: %w", err)
	}
	if tok.Error != "" || tok.IDToken == "" {
		return "", "", "", fmt.Errorf("dex token: status %d error %q", resp.StatusCode, tok.Error)
	}
	idToken, err := p.Verifier(&oidc.Config{ClientID: o.cfg.DexClientID}).Verify(ctx, tok.IDToken)
	if err != nil {
		return "", "", "", fmt.Errorf("dex id_token: %w", err)
	}
	var claims struct {
		Name              string `json:"name"`
		PreferredUsername string `json:"preferred_username"`
		Federated         struct {
			ConnectorID string `json:"connector_id"`
			UserID      string `json:"user_id"`
		} `json:"federated_claims"`
	}
	if err := idToken.Claims(&claims); err != nil {
		return "", "", "", fmt.Errorf("dex id_token: claims: %w", err)
	}
	if claims.Federated.ConnectorID == "" || claims.Federated.UserID == "" {
		return "", "", "", fmt.Errorf("dex id_token: no federated_claims (scope %q not honoured)", dexScope)
	}
	name = claims.Name
	if name == "" {
		name = claims.PreferredUsername
	}
	return claims.Federated.ConnectorID, claims.Federated.UserID, name, nil
}
