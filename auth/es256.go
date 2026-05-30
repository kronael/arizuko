package auth

// ES256 token primitives. authd is the sole signer (see specs/5/1); this file
// holds the building blocks it uses. Backends never link the signing path —
// they only call VerifyToken (jwks.go) against cached public JWKs.
//
// Named distinctly from the legacy HS256 Claims/Identity (jwt.go, identity.go)
// so this slice is additive. The gated-split cutover retires those and may
// rename these; until then both coexist.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"

	jose "github.com/go-jose/go-jose/v4"
)

const Issuer = "authd"

var (
	ErrScopeTooBroad = errors.New("requested scope exceeds bound")
	ErrNoSigningKey  = errors.New("no active signing key")
)

// TokenClaims is the payload authd signs. Generic: the library does not know
// what scope strings or Extra keys mean (folder lives in Extra for arizuko).
//
// Extra entries serialize as TOP-LEVEL claims (e.g. "arz/folder"), NOT a nested
// "extra" object — that is what routd/runed read back as Subject.Extra. The
// Extra map KEY is the full claim name; the round-trip is exact. See the custom
// MarshalJSON/UnmarshalJSON below. Reserved claim names (reservedClaims) never
// land in Extra.
type TokenClaims struct {
	Sub       string            `json:"sub"`
	Typ       string            `json:"typ,omitempty"` // "user" | "service" | "downscoped" (claim, not JWS header)
	Scope     []string          `json:"scope,omitempty"`
	Aud       string            `json:"aud,omitempty"`
	Iss       string            `json:"iss"`
	Iat       int64             `json:"iat"`
	Nbf       int64             `json:"nbf"`
	Exp       int64             `json:"exp"`
	Jti       string            `json:"jti,omitempty"`        // unique token id; audit + downscope parent ref
	ParentJTI string            `json:"parent_jti,omitempty"` // downscoped tokens only
	Extra     map[string]string `json:"-"`
}

// reservedClaims are the standard JWT fields TokenClaims owns; every other
// top-level claim is an Extra entry (the marshal/unmarshal boundary).
var reservedClaims = map[string]struct{}{
	"sub": {}, "typ": {}, "scope": {}, "aud": {}, "iss": {}, "iat": {}, "nbf": {}, "exp": {},
	"jti": {}, "parent_jti": {},
}

// claimsAlias avoids the MarshalJSON/UnmarshalJSON recursion on TokenClaims.
type claimsAlias TokenClaims

// MarshalJSON emits the standard fields plus each Extra entry as a top-level
// claim. A reserved-name Extra key is dropped (it would shadow a real claim).
func (c TokenClaims) MarshalJSON() ([]byte, error) {
	base, err := json.Marshal(claimsAlias(c))
	if err != nil {
		return nil, err
	}
	if len(c.Extra) == 0 {
		return base, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(base, &m); err != nil {
		return nil, err
	}
	for k, v := range c.Extra {
		if _, reserved := reservedClaims[k]; reserved {
			continue
		}
		rv, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		m[k] = rv
	}
	return json.Marshal(m)
}

// UnmarshalJSON reads the standard fields, then folds every non-reserved
// string-valued top-level claim into Extra so Subject.Extra["arz/folder"]
// reflects the minted claim.
func (c *TokenClaims) UnmarshalJSON(b []byte) error {
	var a claimsAlias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*c = TokenClaims(a)
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return err
	}
	for k, raw := range m {
		if _, reserved := reservedClaims[k]; reserved {
			continue
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			continue // non-string custom claims are ignored, not an error
		}
		if c.Extra == nil {
			c.Extra = map[string]string{}
		}
		c.Extra[k] = s
	}
	return nil
}

// Subject is the verified result of a token: what VerifyToken returns.
type Subject struct {
	Sub       string
	Typ       string // "user" | "service" | "downscoped" — drives verifier policy (spec 5/1)
	Scope     []string
	Aud       string
	Iss       string
	JTI       string
	ParentJTI string
	Extra     map[string]string
	IssuedAt  time.Time
	Expires   time.Time
}

// SigningKey is one ES256 keypair authd holds. The private key signs; the
// public half is published in the JWKS under Kid.
type SigningKey struct {
	Kid  string
	Priv *ecdsa.PrivateKey
}

// NewJTI returns a random 128-bit token id (base64url), used for audit
// correlation and as a downscope parent reference.
func NewJTI() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// NewSigningKey generates a fresh P-256 keypair with the given kid.
func NewSigningKey(kid string) (*SigningKey, error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	return &SigningKey{Kid: kid, Priv: priv}, nil
}

// Sign mints a compact ES256 JWT for claims with the given TTL. iss is forced
// to Issuer; iat/nbf/exp are set from now. The kid rides the protected header
// so verifiers pick the right public key on rotation.
func (k *SigningKey) Sign(c TokenClaims, ttl time.Duration) (string, error) {
	now := time.Now()
	c.Iss = Issuer
	c.Iat = now.Unix()
	c.Nbf = now.Unix()
	c.Exp = now.Add(ttl).Unix()
	if c.Jti == "" {
		c.Jti = NewJTI()
	}
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	opts := (&jose.SignerOptions{}).WithType("JWT")
	opts.WithHeader("kid", k.Kid)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: k.Priv}, opts)
	if err != nil {
		return "", err
	}
	jws, err := signer.Sign(payload)
	if err != nil {
		return "", err
	}
	return jws.CompactSerialize()
}

// MintForSubject is authoritative issuance: authd minting a token for a target
// subject after OAuth login or service-key exchange. The granted scope is
// bounded by the TARGET subject's grants snapshot (not any caller's scope) —
// granted = requested ∩ targetGrants, or all of targetGrants when requested is
// empty.
func (k *SigningKey) MintForSubject(targetGrants []string, c TokenClaims, ttl time.Duration) (string, error) {
	if len(c.Scope) == 0 {
		c.Scope = targetGrants
	} else {
		c.Scope = intersectScopes(c.Scope, targetGrants)
	}
	return k.Sign(c, ttl)
}

// MintNarrower is delegated issuance: an agent downscoping its own token. The
// requested scope must be a strict-or-equal subset of the parent's effective
// scope; anything broader errors. Backs the mint_token MCP tool.
func (k *SigningKey) MintNarrower(parentScope []string, c TokenClaims, ttl time.Duration) (string, error) {
	for _, want := range c.Scope {
		if !scopeCoveredBy(parentScope, want) {
			return "", ErrScopeTooBroad
		}
	}
	if len(c.Scope) == 0 {
		c.Scope = append([]string(nil), parentScope...)
	}
	c.Typ = "downscoped"
	return k.Sign(c, ttl)
}
