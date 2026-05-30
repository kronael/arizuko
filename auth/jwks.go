package auth

// JWKS publication + offline verification. authd serves PublicJWKS at
// /v1/keys; backends verify against it (specs/5/1 "offline verification, no
// hot-path hop").
//
// Two verify paths, one VerifyToken entrypoint:
//   - Backends (routd/runed/proxyd) call FetchKeys(authdURL) → a KeySet backed
//     by go-oidc's RemoteKeySet, which fetches /v1/keys and handles caching,
//     rotation, and refresh coalescing (no per-request hop, no kid-miss
//     stampede). go-oidc, NOT IDTokenVerifier — we mint our own tokens, so we
//     verify the signature here and check claims ourselves (subjectFromPayload).
//   - authd itself and tests hold the public keys directly (NewKeySet); there
//     is no network and no stampede, so that path verifies in-process with
//     go-jose against an immutable key map.

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
)

const tokenClockSkew = 30 * time.Second

// KeySet verifies authd-minted ES256 tokens. Exactly one path is active:
// remote (RemoteKeySet over /v1/keys, for backends) or local (an immutable
// public-key map, for authd-self and tests). Safe for concurrent use — remote
// is internally synchronized; local is read-only after construction.
type KeySet struct {
	remote *oidc.RemoteKeySet
	keys   map[string]*ecdsa.PublicKey
}

// NewKeySet builds a local KeySet from public keys — used in-process by authd
// (which already holds the keys) and by tests. No network, no refresh.
func NewKeySet(pub map[string]*ecdsa.PublicKey) *KeySet {
	keys := make(map[string]*ecdsa.PublicKey, len(pub))
	for k, v := range pub {
		keys[k] = v
	}
	return &KeySet{keys: keys}
}

// FetchKeys returns a KeySet backed by go-oidc's RemoteKeySet against
// authdURL+/v1/keys. RemoteKeySet fetches lazily on first verify and owns
// caching + rotation + refresh coalescing; a bad-kid flood cannot stampede
// authd. ctx scopes the keyset's background HTTP for the daemon's lifetime.
func FetchKeys(ctx context.Context, authdURL string) (*KeySet, error) {
	url := strings.TrimRight(authdURL, "/") + "/v1/keys"
	return &KeySet{remote: oidc.NewRemoteKeySet(ctx, url)}, nil
}

func (ks *KeySet) lookup(kid string) (*ecdsa.PublicKey, bool) {
	pub, ok := ks.keys[kid]
	return pub, ok
}

// verifyLocal checks the JWS signature against the in-memory key map with
// go-jose, pinning ES256 (closes alg-confusion). Returns the payload.
func (ks *KeySet) verifyLocal(token string) ([]byte, error) {
	jws, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(jws.Signatures) != 1 {
		return nil, ErrInvalidToken
	}
	pub, ok := ks.lookup(jws.Signatures[0].Header.KeyID)
	if !ok {
		return nil, ErrInvalidToken
	}
	return jws.Verify(pub)
}

// VerifyToken verifies a compact ES256 JWT and returns the Subject. The
// signature is checked by the active path (RemoteKeySet or local go-jose);
// claims (nbf/exp) are checked here — we never use IDTokenVerifier for
// arizuko-minted tokens.
func VerifyToken(token string, ks *KeySet) (Subject, error) {
	var payload []byte
	var err error
	if ks.remote != nil {
		// Pin ES256 BEFORE RemoteKeySet.VerifySignature, which does NOT
		// validate alg (go-oidc warns against using it directly). Without
		// this, a poisoned/MITM'd JWKS serving an `oct` key + an HS256 token
		// would verify — alg/key-type confusion. The pre-parse rejects any
		// non-ES256 token, mirroring the local path.
		if _, perr := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256}); perr != nil {
			return Subject{}, ErrInvalidToken
		}
		payload, err = ks.remote.VerifySignature(context.Background(), token)
	} else {
		payload, err = ks.verifyLocal(token)
	}
	if err != nil {
		return Subject{}, ErrInvalidToken
	}
	return subjectFromPayload(payload)
}

// subjectFromPayload parses verified claims and enforces the time window.
func subjectFromPayload(payload []byte) (Subject, error) {
	var c TokenClaims
	if err := json.Unmarshal(payload, &c); err != nil {
		return Subject{}, ErrInvalidToken
	}
	// Pin the issuer — only authd-minted tokens are accepted (specs/5/1).
	if c.Iss != Issuer {
		return Subject{}, ErrInvalidToken
	}
	// iat/nbf/exp are required claims; a missing time bound is a reject,
	// never "no constraint" (no fail-open).
	if c.Exp == 0 || c.Nbf == 0 {
		return Subject{}, ErrInvalidToken
	}
	now := time.Now().Unix()
	skew := int64(tokenClockSkew.Seconds())
	if now+skew < c.Nbf {
		return Subject{}, ErrInvalidToken
	}
	if now > c.Exp+skew {
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
