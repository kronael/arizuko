package dynstore

import (
	"fmt"
	"os"
)

// The environment an instance's daemons share. compose generation writes these
// into every daemon's env file, and each daemon reads them here — one
// declaration, many readers. A daemon never derives them from a path.
const (
	EnvURL      = "DYNAMODB_URL"
	EnvRegion   = "AWS_REGION"
	EnvKeyID    = "AWS_ACCESS_KEY_ID"
	EnvSecret   = "AWS_SECRET_ACCESS_KEY"
	EnvCABundle = "AWS_CA_BUNDLE"
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
	return Dial(Config{
		Endpoint: url,
		Region:   os.Getenv(EnvRegion),
		Prefix:   prefix,
		KeyID:    os.Getenv(EnvKeyID),
		Secret:   os.Getenv(EnvSecret),
		CABundle: os.Getenv(EnvCABundle),
	})
}
