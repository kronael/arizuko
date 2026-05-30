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
//
// retiredAt (local path only) records when a kid was retired. A retired key
// stays servable through its overlap window so tokens MINTED BEFORE retirement
// keep verifying, but a token whose iat is AFTER retired_at could only have
// been freshly minted by a holder of the retired private key — i.e. a thief.
// VerifyToken rejects those, closing the retired-key forgery window.
type KeySet struct {
	remote    *oidc.RemoteKeySet
	keys      map[string]*ecdsa.PublicKey
	retiredAt map[string]time.Time // kid -> retired_at; absent = active (no iat cap)
}

// NewKeySet builds a local KeySet from public keys — used in-process by authd
// (which already holds the keys) and by tests. No network, no refresh.
func NewKeySet(pub map[string]*ecdsa.PublicKey) *KeySet {
	return NewKeySetWithRetirement(pub, nil)
}

// NewKeySetWithRetirement is NewKeySet plus per-kid retirement times. A token
// signed by a kid in retiredAt is rejected when its iat is after that time
// (forgery with a stolen retired key); a kid absent from retiredAt is the
// active key — no iat cap.
func NewKeySetWithRetirement(pub map[string]*ecdsa.PublicKey, retiredAt map[string]time.Time) *KeySet {
	keys := make(map[string]*ecdsa.PublicKey, len(pub))
	for k, v := range pub {
		keys[k] = v
	}
	ret := make(map[string]time.Time, len(retiredAt))
	for k, v := range retiredAt {
		ret[k] = v
	}
	return &KeySet{keys: keys, retiredAt: ret}
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
// go-jose, pinning ES256 (closes alg-confusion). Returns the payload + signing
// kid (the kid backs the retired-key forgery check in VerifyToken).
func (ks *KeySet) verifyLocal(token string) (payload []byte, kid string, err error) {
	jws, err := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
	if err != nil || len(jws.Signatures) != 1 {
		return nil, "", ErrInvalidToken
	}
	kid = jws.Signatures[0].Header.KeyID
	pub, ok := ks.lookup(kid)
	if !ok {
		return nil, "", ErrInvalidToken
	}
	payload, err = jws.Verify(pub)
	return payload, kid, err
}

// VerifyToken verifies a compact ES256 JWT and returns the Subject. The
// signature is checked by the active path (RemoteKeySet or local go-jose);
// claims (nbf/exp) are checked here — we never use IDTokenVerifier for
// arizuko-minted tokens.
func VerifyToken(token string, ks *KeySet) (Subject, error) {
	var payload []byte
	var kid string
	var err error
	if ks.remote != nil {
		// Pin ES256 BEFORE RemoteKeySet.VerifySignature, which does NOT
		// validate alg (go-oidc warns against using it directly). Without
		// this, a poisoned/MITM'd JWKS serving an `oct` key + an HS256 token
		// would verify — alg/key-type confusion. The pre-parse rejects any
		// non-ES256 token, mirroring the local path. We also lift the signing
		// kid out of the parsed header so the retired-key forgery check below
		// fires on the remote path too (without it, kid="" disables the check).
		jws, perr := jose.ParseSigned(token, []jose.SignatureAlgorithm{jose.ES256})
		if perr != nil || len(jws.Signatures) != 1 {
			return Subject{}, ErrInvalidToken
		}
		kid = jws.Signatures[0].Header.KeyID
		payload, err = ks.remote.VerifySignature(context.Background(), token)
	} else {
		payload, kid, err = ks.verifyLocal(token)
	}
	if err != nil {
		return Subject{}, ErrInvalidToken
	}
	sub, err := subjectFromPayload(payload)
	if err != nil {
		return Subject{}, err
	}
	// Retired-key forgery window: a key keeps serving after retirement so
	// tokens it signed BEFORE retirement still verify, but a token whose iat
	// is after retired_at must have been minted by a thief holding the retired
	// private key. Reject it. retiredAt is populated on the local path
	// (NewKeySetWithRetirement); on the remote path it is empty unless the
	// verifier was built with retirement times, so the kid lookup is a no-op
	// for the common RemoteKeySet case — but when present, it fires uniformly.
	if retired, ok := ks.retiredAt[kid]; ok && sub.IssuedAt.After(retired) {
		return Subject{}, ErrInvalidToken
	}
	return sub, nil
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
	// never "no constraint" (no fail-open). iat==0 would zero IssuedAt and
	// defeat VerifyToken's retired-key forgery check, so reject it too.
	if c.Exp == 0 || c.Nbf == 0 || c.Iat == 0 {
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
		JTI: c.Jti, ParentJTI: c.ParentJTI,
		Extra: c.Extra, IssuedAt: time.Unix(c.Iat, 0), Expires: time.Unix(c.Exp, 0),
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
