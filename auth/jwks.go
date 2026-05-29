package auth

// JWKS publication + offline verification. authd serves PublicJWKS at
// /v1/keys; every backend caches it via FetchKeys and verifies in-process with
// VerifyToken — a pure function over (token, KeySet), no network hop per
// request (specs/5/1 "offline verification, no hot-path hop").

import (
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const tokenClockSkew = 30 * time.Second

// KeySet caches authd's public JWKs, indexed by kid. Safe for concurrent use.
// Refreshes from authdURL on a kid miss (rotation) or never if authdURL is "".
type KeySet struct {
	authdURL string
	mu       sync.RWMutex
	keys     map[string]*ecdsa.PublicKey
}

// NewKeySet builds a KeySet directly from public keys — used in-process by
// authd (which already holds the keys) and by tests. authdURL is empty so it
// never tries to refresh over the network.
func NewKeySet(pub map[string]*ecdsa.PublicKey) *KeySet {
	keys := make(map[string]*ecdsa.PublicKey, len(pub))
	for k, v := range pub {
		keys[k] = v
	}
	return &KeySet{keys: keys}
}

// FetchKeys fetches authd's public JWKs from authdURL+/v1/keys and returns a
// KeySet that refreshes against the same URL on a kid miss.
func FetchKeys(authdURL string) (*KeySet, error) {
	ks := &KeySet{authdURL: strings.TrimRight(authdURL, "/"), keys: map[string]*ecdsa.PublicKey{}}
	if err := ks.refresh(); err != nil {
		return nil, err
	}
	return ks, nil
}

func (ks *KeySet) refresh() error {
	if ks.authdURL == "" {
		return errors.New("keyset has no authd URL to refresh from")
	}
	resp, err := httpClient.Get(ks.authdURL + "/v1/keys")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	var set jose.JSONWebKeySet
	if err := json.Unmarshal(body, &set); err != nil {
		return err
	}
	parsed := map[string]*ecdsa.PublicKey{}
	for _, jwk := range set.Keys {
		if pub, ok := jwk.Key.(*ecdsa.PublicKey); ok {
			parsed[jwk.KeyID] = pub
		}
	}
	ks.mu.Lock()
	ks.keys = parsed
	ks.mu.Unlock()
	return nil
}

func (ks *KeySet) lookup(kid string) (*ecdsa.PublicKey, bool) {
	ks.mu.RLock()
	pub, ok := ks.keys[kid]
	ks.mu.RUnlock()
	if ok {
		return pub, true
	}
	if ks.authdURL == "" {
		return nil, false
	}
	if err := ks.refresh(); err != nil {
		return nil, false
	}
	ks.mu.RLock()
	pub, ok = ks.keys[kid]
	ks.mu.RUnlock()
	return pub, ok
}

// VerifyToken verifies a compact ES256 JWT against the cached public keys and
// returns the Subject. Errors on bad signature, unknown kid, or expiry.
func VerifyToken(token string, ks *KeySet) (Subject, error) {
	jws, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil {
		return Subject{}, ErrInvalidToken
	}
	if len(jws.Signatures) != 1 {
		return Subject{}, ErrInvalidToken
	}
	kid := jws.Signatures[0].Header.KeyID
	pub, ok := ks.lookup(kid)
	if !ok {
		return Subject{}, ErrInvalidToken
	}
	payload, err := jws.Verify(pub)
	if err != nil {
		return Subject{}, ErrInvalidToken
	}
	var c TokenClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Subject{}, ErrInvalidToken
	}
	now := time.Now().Unix()
	skew := int64(tokenClockSkew.Seconds())
	if c.Nbf != 0 && now+skew < c.Nbf {
		return Subject{}, ErrInvalidToken
	}
	if c.Exp != 0 && now > c.Exp+skew {
		return Subject{}, ErrExpiredToken
	}
	return Subject{
		Sub: c.Sub, Scope: c.Scope, Aud: c.Aud, Iss: c.Iss,
		Extra: c.Extra, Expires: time.Unix(c.Exp, 0),
	}, nil
}

// VerifyHTTP extracts a Bearer token from r and verifies it.
func VerifyHTTP(r *http.Request, ks *KeySet) (Subject, error) {
	hdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(hdr, "Bearer ") {
		return Subject{}, ErrInvalidToken
	}
	return VerifyToken(strings.TrimPrefix(hdr, "Bearer "), ks)
}

// PublicJWKS serializes the public halves of keys as a JWKS document for
// /v1/keys. Each key advertises alg=ES256, use=sig, and its kid.
func PublicJWKS(keys ...*SigningKey) ([]byte, error) {
	set := jose.JSONWebKeySet{}
	for _, k := range keys {
		set.Keys = append(set.Keys, jose.JSONWebKey{
			Key:       k.Priv.Public(),
			KeyID:     k.Kid,
			Algorithm: string(jose.ES256),
			Use:       "sig",
		})
	}
	return json.Marshal(set)
}
