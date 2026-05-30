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
type httpBroker struct {
	authdURL     string
	serviceToken string // runed's service:runed token (the downscope parent)
	http         *http.Client
}

// NewHTTPBroker builds the production broker against authd. serviceToken is
// runed's service:runed token exchanged at boot.
func NewHTTPBroker(authdURL, serviceToken string) Broker {
	return &httpBroker{
		authdURL:     strings.TrimRight(authdURL, "/"),
		serviceToken: serviceToken,
		http:         &http.Client{Timeout: 10 * time.Second},
	}
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.authdURL+"/v1/tokens", bytes.NewReader(raw))
	if err != nil {
		return "", "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+b.serviceToken)
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
