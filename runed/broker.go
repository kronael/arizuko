package runed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/kronael/arizuko/types"
)

// httpBroker brokers a downscoped capability token per spawn by calling
// authd's downscope endpoint (spec 5/P § brokering / 5/1 § POST /v1/tokens
// downscope mode). runed mints nothing — authd enforces scope ⊆ parent and
// folder ⊆ parent-folder. runed persists only the returned jti; the raw JWS
// rides back to the agent over the SO_PEERCRED-gated MCP socket.
//
// tokenFn, when set, supplies a live (auto-refreshing) service:runed token
// from auth.ServiceToken — the downscope parent. When nil, the static
// serviceToken string is used (local-dev / standalone, where authd's
// issuer-pin is not in play).
type httpBroker struct {
	authdURL     string
	serviceToken string                                // static fallback parent token
	tokenFn      func(context.Context) (string, error) // live service token; wins when non-nil
	http         *http.Client
}

// NewHTTPBroker builds the production broker against authd. serviceToken is
// runed's static service:runed token (local-dev fallback).
func NewHTTPBroker(authdURL, serviceToken string) Broker {
	return &httpBroker{
		authdURL:     strings.TrimRight(authdURL, "/"),
		serviceToken: serviceToken,
		http:         &http.Client{Timeout: 10 * time.Second},
	}
}

// NewHTTPBrokerWithSource builds the production broker with a refreshing token
// source (auth.ServiceToken) as the downscope parent — the boot-exchange path
// for the HMAC→ES256 cutover. tokenFn is called per broker request; the
// TokenSource caches + refreshes so this is not a per-call authd hop.
func NewHTTPBrokerWithSource(authdURL string, tokenFn func(context.Context) (string, error)) Broker {
	return &httpBroker{
		authdURL: strings.TrimRight(authdURL, "/"),
		tokenFn:  tokenFn,
		http:     &http.Client{Timeout: 10 * time.Second},
	}
}

// parentToken returns the live service token (tokenFn) when configured,
// else the static fallback.
func (b *httpBroker) parentToken(ctx context.Context) (string, error) {
	if b.tokenFn != nil {
		return b.tokenFn(ctx)
	}
	return b.serviceToken, nil
}

func (b *httpBroker) Broker(ctx context.Context, sub types.UserSub, folder string, want []types.Scope, ttl time.Duration) (string, string, string, error) {
	reqBody := map[string]any{
		"typ":         "downscoped",
		"sub":         string(sub),
		"scope":       want,
		"folder":      folder,
		"ttl_seconds": int(ttl.Seconds()),
	}
	raw, _ := json.Marshal(reqBody)
	parent, err := b.parentToken(ctx)
	if err != nil {
		return "", "", "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.authdURL+"/v1/tokens", bytes.NewReader(raw))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+parent)
	resp, err := b.http.Do(req)
	if err != nil {
		return "", "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("authd downscope: %d", resp.StatusCode)
	}
	var out struct {
		Token     string `json:"token"`
		JTI       string `json:"jti"`
		ExpiresAt string `json:"expires_at"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", "", err
	}
	return out.Token, out.JTI, out.ExpiresAt, nil
}
