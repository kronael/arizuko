package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
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

	mu    sync.Mutex
	state credState
	err   error
	user  driveUser

	src  oauth2.TokenSource
	http *http.Client
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
	user, state, err := c.probe(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.state, c.err = state, err
	if err == nil {
		c.user = user
	}
	return err
}

func (c *credential) probe(ctx context.Context) (driveUser, credState, error) {
	if _, err := c.src.Token(); err != nil {
		var refusal *oauth2.RetrieveError
		if errors.As(err, &refusal) {
			return driveUser{}, credRefused, fmt.Errorf("google refused the key: %w", err)
		}
		return driveUser{}, credUnreachable, fmt.Errorf("google token endpoint did not answer: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.apiBase+"/drive/v3/about?fields=user", nil)
	if err != nil {
		return driveUser{}, credUnreachable, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return driveUser{}, credUnreachable, fmt.Errorf("drive about.get did not answer: %w", err)
	}
	// body read below
	defer resp.Body.Close() //nolint:errcheck
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return driveUser{}, credRefused, fmt.Errorf("google refused the key: Drive about.get %s: %s",
			resp.Status, strings.TrimSpace(string(raw)))
	}
	var about struct {
		User driveUser `json:"user"`
	}
	if err := json.Unmarshal(raw, &about); err != nil {
		return driveUser{}, credRefused, fmt.Errorf("drive about.get answered with unreadable JSON: %w", err)
	}
	return about.User, credOK, nil
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
	return c.state == credRefused || c.state == credUnreachable
}

// status is the credential's line on the dashboard.
func (c *credential) status() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch c.state {
	case credOK:
		return "ok"
	case credMissing:
		return "no key: " + c.err.Error() + "; gdrid reads nothing and replies to nothing"
	case credMalformed:
		return "key will not parse; fix GDRIVE_SA_KEY_JSON and restart gdrid: " + c.err.Error()
	case credRefused, credUnreachable:
		return c.err.Error() + "; gdrid keeps asking"
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
