package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/jwt"
)

// driveScope is the one scope that can both read and reply on a shared file:
// drive.readonly cannot reply and drive.file sees only Picker-opened files.
const driveScope = "https://www.googleapis.com/auth/drive"
const sheetsScope = "https://www.googleapis.com/auth/spreadsheets.readonly"

const googleTimeout = 15 * time.Second

// credState is where the service-account key stands. Missing and malformed
// never heal without a new key; refused and unreachable may heal on their own.
type credState int

const (
	credUnchecked credState = iota
	credMissing
	credMalformed
	credRefused
	credUnreachable
	credOK
)

// driveUser is the identity about.get reports for the credential in use.
type driveUser struct {
	EmailAddress string `json:"emailAddress"`
	PermissionID string `json:"permissionId"`
}

type credential struct {
	apiBase string

	mu                  sync.Mutex
	state               credState
	err                 error
	user                driveUser
	missingAccount      sync.Once
	driveAPI, sheetsAPI apiState

	sheetsHTTP *http.Client
	src        oauth2.TokenSource
	http       *http.Client
}

// newCredential reads the key once. A key that is absent or will not parse
// leaves the credential in a state that never touches the network.
func newCredential(ctx context.Context, cfg config) *credential {
	c := &credential{apiBase: cfg.APIBase}
	if strings.TrimSpace(cfg.KeyJSON) == "" {
		c.state, c.err = credMissing, errors.New("GDRIVE_SA_KEY_JSON is not set")
		return c
	}
	jc, err := parseKey(cfg.KeyJSON, cfg.Subject)
	if err != nil {
		c.state, c.err = credMalformed, err
		return c
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Timeout: googleTimeout})
	c.src = oauth2.ReuseTokenSource(nil, jc.TokenSource(ctx))
	c.http = oauth2.NewClient(ctx, c.src)
	c.http.Timeout = googleTimeout
	sheetsConfig := *jc
	sheetsConfig.Scopes = []string{sheetsScope}
	c.sheetsHTTP = oauth2.NewClient(ctx, oauth2.ReuseTokenSource(nil, sheetsConfig.TokenSource(ctx)))
	c.sheetsHTTP.Timeout = googleTimeout
	return c
}

type keyFile struct {
	Type         string `json:"type"`
	ClientEmail  string `json:"client_email"`
	PrivateKey   string `json:"private_key"`
	PrivateKeyID string `json:"private_key_id"`
	TokenURI     string `json:"token_uri"`
}

// parseKey turns the downloaded service-account JSON into the JWT config that
// signs the assertion. Everything that would make signing fail later is
// refused here, so a key that will not parse is never mistaken for a refusal
// by Google.
func parseKey(raw, subject string) (*jwt.Config, error) {
	var k keyFile
	if err := json.Unmarshal([]byte(raw), &k); err != nil {
		return nil, fmt.Errorf("service account key is not JSON: %w", err)
	}
	switch {
	case k.Type != "service_account":
		return nil, fmt.Errorf("service account key has type %q, want service_account", k.Type)
	case k.ClientEmail == "":
		return nil, errors.New("service account key has no client_email")
	case k.TokenURI == "":
		return nil, errors.New("service account key has no token_uri")
	}
	if err := checkPrivateKey(k.PrivateKey); err != nil {
		return nil, err
	}
	return &jwt.Config{
		Email:        k.ClientEmail,
		PrivateKey:   []byte(k.PrivateKey),
		PrivateKeyID: k.PrivateKeyID,
		Subject:      subject,
		Scopes:       []string{driveScope},
		TokenURL:     k.TokenURI,
	}, nil
}

func checkPrivateKey(pemKey string) error {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return errors.New("service account private_key is not PEM")
	}
	if _, err := x509.ParsePKCS8PrivateKey(block.Bytes); err != nil {
		if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return errors.New("service account private_key is not a PKCS#8 or PKCS#1 key")
		}
	}
	return nil
}

// verify asks Google for an access token and then for the account's identity.
// The second call is what proves the Drive API is enabled for the key's
// project: a token exchange succeeds without it.
func (c *credential) verify(ctx context.Context) error {
	if c.src == nil {
		return c.stored()
	}
	user, state, drive, err := c.probe(ctx)
	sheets := probeAPI(ctx, c.sheetsHTTP, c.apiBase+"/v4/spreadsheets/!", "Sheets")
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state, c.err = state, err
	c.driveAPI, c.sheetsAPI = drive, sheets
	if err == nil {
		c.user = user
		if user.EmailAddress == "" {
			c.missingAccount.Do(func() {
				slog.Warn("Drive account email unavailable; mention detection disabled")
			})
		}
	}
	return err
}

func (c *credential) probe(ctx context.Context) (driveUser, credState, apiState, error) {
	if _, err := c.src.Token(); err != nil {
		var refusal *oauth2.RetrieveError
		if errors.As(err, &refusal) {
			return driveUser{}, credRefused, apiState{}, fmt.Errorf("google refused the key: %w", err)
		}
		return driveUser{}, credUnreachable, apiState{}, fmt.Errorf("google token endpoint did not answer: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/drive/v3/about?fields=user", nil)
	if err != nil {
		return driveUser{}, credUnreachable, apiState{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return driveUser{}, credUnreachable, apiState{}, fmt.Errorf("drive about.get did not answer: %w", err)
	}
	// body read below
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	api := responseState(resp.StatusCode, raw)
	if api.state == "off" {
		return driveUser{}, credRefused, api, fmt.Errorf("google refused the key: Drive about.get %s: %s",
			resp.Status, strings.TrimSpace(string(raw)))
	}
	if resp.StatusCode != http.StatusOK {
		return driveUser{}, credOK, api, nil
	}
	var about struct {
		User driveUser `json:"user"`
	}
	if err := json.Unmarshal(raw, &about); err != nil {
		return driveUser{}, credOK, api, nil
	}
	return about.User, credOK, api, nil
}

func (c *credential) stored() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// isRetryable answers whether another verify can change the outcome without a
// new key.
func (c *credential) isRetryable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == credRefused || c.state == credUnreachable ||
		(c.state == credOK && c.user.EmailAddress == "")
}

func (c *credential) usable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.state == credOK
}

// status is the credential's line on the dashboard.
func (c *credential) status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case credOK:
		if c.user.EmailAddress == "" {
			return "ok; account email unavailable, mention detection disabled"
		}
		return "ok"
	case credMissing:
		return "no key: " + c.err.Error() + "; googd reads nothing and replies to nothing"
	case credMalformed:
		return "key will not parse; fix GDRIVE_SA_KEY_JSON and restart googd: " + c.err.Error()
	case credRefused, credUnreachable:
		return c.err.Error() + "; googd keeps asking"
	}
	return "not checked yet"
}

// account is the service account's address, the one an operator shares a
// document with.
func (c *credential) account() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.user.EmailAddress
}

// serviceDisabled reads Google's structured error reason.
func serviceDisabled(raw []byte) bool {
	var body struct {
		Error struct {
			Errors []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) != nil {
		return false
	}
	for _, e := range body.Error.Errors {
		if e.Reason == "SERVICE_DISABLED" {
			return true
		}
	}
	for _, e := range body.Error.Details {
		if e.Reason == "SERVICE_DISABLED" {
			return true
		}
	}
	return false
}

// apiState records the result of an API probe independently of the key.
type apiState struct{ state, reason string }

func responseState(status int, raw []byte) apiState {
	state := apiState{state: "on"}
	var body struct {
		Error struct {
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
			Details []struct {
				Reason string `json:"reason"`
			} `json:"details"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil {
		state.reason = body.Error.Message
		reason := ""
		if len(body.Error.Errors) > 0 {
			reason = body.Error.Errors[0].Reason
		}
		if len(body.Error.Details) > 0 {
			reason = body.Error.Details[0].Reason
		}
		if reason != "" && state.reason != "" {
			state.reason = reason + ": " + state.reason
		} else if reason != "" {
			state.reason = reason
		}
	}
	if status == http.StatusForbidden && serviceDisabled(raw) {
		state.state = "off"
	}
	return state
}

func probeAPI(ctx context.Context, client *http.Client, url, name string) apiState {
	if client == nil {
		return apiState{}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return apiState{reason: err.Error()}
	}
	resp, err := client.Do(req)
	if err != nil {
		return apiState{reason: name + " probe: " + err.Error()}
	}
	defer resp.Body.Close() //nolint:errcheck
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return apiState{reason: err.Error()}
	}
	return responseState(resp.StatusCode, raw)
}
