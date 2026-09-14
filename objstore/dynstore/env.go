package dynstore

import (
	"encoding/base64"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"
)

// The environment an instance's daemons share. compose generation writes these
// into every daemon's env file, and each daemon reads them here — one
// declaration, many readers. A daemon never derives them from a path.
const (
	EnvURL    = "DYNAMODB_URL"
	EnvRegion = "AWS_REGION"
	EnvKeyID  = "AWS_ACCESS_KEY_ID"
	EnvSecret = "AWS_SECRET_ACCESS_KEY"
	// EnvCA carries the endpoint certificate itself, base64-encoded, because a
	// daemon reads its environment and mounts no file of its own.
	EnvCA = "STORE_CA"
)

// Prefix is what one owner's entities are named under. A server holds every
// owner of an instance at once, so the table name carries the owner in front of
// the entity: `routd.acl`. The owner is a name its daemon was configured with,
// never one derived from a path (CLAUDE.md).
func Prefix(owner string) string { return owner + "." }

// URL is the endpoint this daemon's store lives on, or "" when the instance
// still keeps its owners in files. It is the branch every daemon's boot takes.
func URL() string { return os.Getenv(EnvURL) }

// FromEnv dials URL() for one table prefix. It refuses a half-configured
// instance: an endpoint with no credential is a misconfiguration that would
// otherwise surface as a signing failure on the first write.
func FromEnv(prefix string) (*Store, error) {
	url := URL()
	if url == "" {
		return nil, fmt.Errorf("dynstore: %s names no endpoint", EnvURL)
	}
	ca, err := caFromEnv()
	if err != nil {
		return nil, err
	}
	return Dial(Config{
		Endpoint: url,
		Region:   os.Getenv(EnvRegion),
		Prefix:   prefix,
		KeyID:    os.Getenv(EnvKeyID),
		Secret:   os.Getenv(EnvSecret),
		CA:       ca,
	})
}

// caFromEnv decodes the endpoint certificate. A value that is not base64 is a
// misconfiguration that would otherwise surface as a TLS handshake failure
// against a name nobody typed.
func caFromEnv() ([]byte, error) {
	raw := strings.TrimSpace(os.Getenv(EnvCA))
	if raw == "" {
		return nil, nil
	}
	pem, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("dynstore: %s is not base64: %w", EnvCA, err)
	}
	return pem, nil
}

// TestPrefix mints the table prefix ONE test fixture owns. A server holds every
// fixture of a run at once, so the prefix is what keeps two of them apart, the
// same way Prefix keeps two owners apart on an instance's server.
func TestPrefix() string {
	return fmt.Sprintf("t%d_%d.", time.Now().UnixNano(), rand.Int64())
}
