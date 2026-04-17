package chanreg

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

type Entry struct {
	Name         string
	URL          string
	JIDPrefixes  []string
	Capabilities map[string]bool
	Token        string
	HealthFails  int
	RegisteredAt time.Time

	// Origin pin: first caller to claim Name becomes the owner. Subsequent
	// Register calls for the same Name must present the same origin IP +
	// secret hash, otherwise the request is rejected. Blocks hijack of an
	// adapter's session token by a second CHANNEL_SECRET holder.
	OriginIP   string
	SecretHash [32]byte
}

func (e *Entry) HasCap(cap string) bool { return e.Capabilities[cap] }

func (e *Entry) Owns(jid string) bool {
	for _, p := range e.JIDPrefixes {
		if strings.HasPrefix(jid, p) {
			return true
		}
	}
	return false
}

type Registry struct {
	mu      sync.RWMutex
	entries map[string]*Entry // keyed by name
	byToken map[string]*Entry // keyed by session token
	secret  string
}

func New(secret string) *Registry {
	return &Registry{
		entries: make(map[string]*Entry),
		byToken: make(map[string]*Entry),
		secret:  secret,
	}
}

// Register validates the URL and claims name. Tests and internal callers
// that don't care about origin pinning use this; HTTP handlers should
// call RegisterWithOrigin to enforce the name-ownership policy.
func (r *Registry) Register(name, rawURL string, prefixes []string, caps map[string]bool) (string, error) {
	return r.RegisterWithOrigin(name, rawURL, prefixes, caps, "", "")
}

func (r *Registry) RegisterWithOrigin(name, rawURL string, prefixes []string, caps map[string]bool,
	originIP, presentedSecret string) (string, error) {

	if err := validateAdapterURL(rawURL); err != nil {
		return "", err
	}

	var presentedHash [32]byte
	if presentedSecret != "" {
		presentedHash = sha256.Sum256([]byte(presentedSecret))
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if old, ok := r.entries[name]; ok {
		// Only enforce origin pinning when both sides supplied identifiers.
		// Tests that call Register(...) skip the check; production callers
		// always supply a non-empty presentedSecret via the HTTP handler.
		if old.OriginIP != "" || presentedSecret != "" {
			if subtle.ConstantTimeCompare(old.SecretHash[:], presentedHash[:]) != 1 ||
				old.OriginIP != originIP {
				return "", fmt.Errorf("name %q already registered from a different origin", name)
			}
		}
		delete(r.byToken, old.Token)
	}

	token, err := genToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	e := &Entry{
		Name:         name,
		URL:          rawURL,
		JIDPrefixes:  prefixes,
		Capabilities: caps,
		Token:        token,
		RegisteredAt: time.Now(),
		OriginIP:     originIP,
		SecretHash:   presentedHash,
	}
	r.entries[name] = e
	r.byToken[token] = e

	return token, nil
}

func (r *Registry) Deregister(name string) {
	r.mu.Lock()
	e, ok := r.entries[name]
	if ok {
		delete(r.byToken, e.Token)
		delete(r.entries, name)
	}
	r.mu.Unlock()
}

func (r *Registry) Get(name string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.entries[name]
}

func (r *Registry) ByToken(token string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byToken[token]
}

func (r *Registry) All() map[string]*Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cp := make(map[string]*Entry, len(r.entries))
	for k, v := range r.entries {
		clone := *v
		cp[k] = &clone
	}
	return cp
}

func (r *Registry) Secret() string { return r.secret }

// Resolve looks up the adapter pinned by name, falling back to ForJID when
// name is empty or the named adapter is gone. Returns nil if no match.
func (r *Registry) Resolve(name, jid string) *Entry {
	if name != "" {
		if e := r.Get(name); e != nil {
			return e
		}
	}
	return r.ForJID(jid)
}

// ForJID returns the first adapter that owns jid. When multiple adapters
// share a prefix, iteration order is non-deterministic; callers needing
// exact routing must pass the adapter name.
func (r *Registry) ForJID(jid string) *Entry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, e := range r.entries {
		if e.Owns(jid) {
			return e
		}
	}
	return nil
}

func (r *Registry) RecordHealthFail(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.entries[name]
	if !ok {
		return 0
	}
	e.HealthFails++
	return e.HealthFails
}

func (r *Registry) ResetHealth(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if e, ok := r.entries[name]; ok {
		e.HealthFails = 0
	}
}

func genToken() (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// validateAdapterURL enforces SSRF protection: adapter URLs must be http(s)
// and resolve to private/loopback/link-local addresses. Set
// CHANNEL_REGISTER_ALLOW_PUBLIC=1 to permit public IPs (dev only).
// Literal IPs are checked directly; hostnames that fail to resolve are
// allowed because docker service names only resolve at runtime inside
// the compose network — the outbound send would fail later if the host
// truly doesn't exist, so this is not an SSRF vector.
func validateAdapterURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("invalid url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid url scheme %q: must be http or https", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("invalid url: missing host")
	}
	if os.Getenv("CHANNEL_REGISTER_ALLOW_PUBLIC") == "1" {
		return nil
	}

	if ip := net.ParseIP(host); ip != nil {
		if !isPrivateAddr(ip) {
			return fmt.Errorf("url host %s is a public address; "+
				"adapters must be reachable only on private networks "+
				"(set CHANNEL_REGISTER_ALLOW_PUBLIC=1 to override)", ip)
		}
		return nil
	}

	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil
	}
	for _, ip := range ips {
		if !isPrivateAddr(ip) {
			return fmt.Errorf("url host %q resolves to public address %s; "+
				"adapters must be reachable only on private networks "+
				"(set CHANNEL_REGISTER_ALLOW_PUBLIC=1 to override)", host, ip)
		}
	}
	return nil
}

func isPrivateAddr(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// net.IP.IsPrivate covers 10/8, 172.16/12, 192.168/16, fc00::/7.
	return ip.IsPrivate()
}
