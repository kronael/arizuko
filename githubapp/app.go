package githubapp

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kronael/arizuko/store"
)

func ResolveSource(sources []store.GitHubSource, owner, repo string) (string, error) {
	var repository, org, app bool
	for _, source := range sources {
		kind, name, err := store.ParseGitHubSourceKey(source.Source)
		if err != nil {
			return "", err
		}
		switch kind {
		case store.GitHubSourceRepo:
			repository = repository || strings.EqualFold(name, owner+"/"+repo)
		case store.GitHubSourceOrg:
			org = org || strings.EqualFold(name, owner)
		case store.GitHubSourceApp:
			app = true
		}
	}
	switch {
	case repository:
		return store.GitHubSourceRepo, nil
	case org:
		return store.GitHubSourceOrg, nil
	case app:
		return store.GitHubSourceApp, nil
	default:
		return "", fmt.Errorf("no github source for repository")
	}
}

type token struct {
	Value   string    `json:"token"`
	Expires time.Time `json:"expires_at"`
}

type Client struct {
	id     string
	key    *rsa.PrivateKey
	base   string
	http   *http.Client
	mu     sync.Mutex
	tokens map[string]token
	now    func() time.Time
}

func New(id, privateKey, base string, httpClient *http.Client) (*Client, error) {
	if id == "" || privateKey == "" {
		return nil, fmt.Errorf("github app credentials are required")
	}
	block, _ := pem.Decode([]byte(privateKey))
	if block == nil {
		return nil, fmt.Errorf("invalid github app private key")
	}
	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		parsed, parseErr := x509.ParsePKCS8PrivateKey(block.Bytes)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid github app private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("github app private key must be RSA")
		}
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{id: id, key: key, base: strings.TrimRight(base, "/"), http: httpClient, tokens: make(map[string]token), now: time.Now}, nil
}

func (c *Client) Token(ctx context.Context, owner, repo string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	name := strings.ToLower(owner + "/" + repo)
	now := c.now()
	if cached := c.tokens[name]; cached.Value != "" && now.Add(5*time.Minute).Before(cached.Expires) {
		return cached.Value, nil
	}
	claims, err := json.Marshal(struct {
		Iss string `json:"iss"`
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
	}{c.id, now.Add(-time.Minute).Unix(), now.Add(9 * time.Minute).Unix()})
	if err != nil {
		return "", err
	}
	enc := base64.RawURLEncoding
	unsigned := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	hash := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.key, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("sign github app JWT: %w", err)
	}
	jwt := unsigned + "." + enc.EncodeToString(signature)
	var installation struct {
		ID int64 `json:"id"`
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/installation"
	if err := c.request(ctx, http.MethodGet, path, jwt, nil, &installation); err != nil {
		return "", err
	}
	if installation.ID <= 0 {
		return "", fmt.Errorf("github installation has no id")
	}
	body, err := json.Marshal(struct {
		Repositories []string `json:"repositories"`
	}{[]string{repo}})
	if err != nil {
		return "", err
	}
	var result token
	if err := c.request(ctx, http.MethodPost, fmt.Sprintf("/app/installations/%d/access_tokens", installation.ID), jwt, body, &result); err != nil {
		return "", err
	}
	if result.Value == "" || !result.Expires.After(now.Add(5*time.Minute)) {
		return "", fmt.Errorf("github installation token is empty or expires too soon")
	}
	c.tokens[name] = result
	return result.Value, nil
}

func (c *Client) request(ctx context.Context, method, path, jwt string, body []byte, result any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "arizuko")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("github app request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github app request: %s", resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(result); err != nil {
		return fmt.Errorf("github app response: %w", err)
	}
	return nil
}
