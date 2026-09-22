package resreg

// The archive INDEX (spec 5/8 "The full-instance archive"): archive.yaml's
// shape, the consistency level it declares, and the consistent subsystem
// snapshot the checksum is stamped from. It reuses Export/EmitYAML/Checksum
// verbatim. The per-table halves are NOT here: each value an archive carries
// beyond the manifest is a second reader of ONE table, and lives beside that
// table's first reader in store.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// ArchiveFormatVersion is archive.yaml's own format version, bumped only when
// the document's shape changes (spec 5/8 "Cross-instance portability").
// `archive apply` refuses an archive whose stamped format_version exceeds it.
const ArchiveFormatVersion = 1

// Consistency levels an archive.yaml declares (spec 5/8 "Consistency
// levels"). live: per-subsystem snapshots, the archive as a whole a smear
// across wall-clock time. quiesced: the operator stopped the instance first;
// the flag only stamps the metadata.
const (
	ConsistencyLive     = "live"
	ConsistencyQuiesced = "quiesced"
)

// ArchiveManifest is archive.yaml: the archive's top-level index. Per
// subsystem it records the snapshot's start timestamp, never a claim of one
// cross-archive point-in-time image.
type ArchiveManifest struct {
	FormatVersion int                        `yaml:"format_version"`
	Consistency   string                     `yaml:"consistency"`
	CreatedAt     string                     `yaml:"created_at"`
	Subsystems    map[string]ArchiveSnapshot `yaml:"subsystems"`
}

// ArchiveSnapshot is one subsystem's entry in archive.yaml.
type ArchiveSnapshot struct {
	SnapshotAt string `yaml:"snapshot_at"`
	Checksum   string `yaml:"checksum"`
}

// ExportSnapshot exports one subsystem's manifest-visible projection as ONE
// consistent read (spec 5/8 "Consistency levels"), and answers the manifest,
// its checksum and the snapshot's start timestamp.
func ExportSnapshot(ctx context.Context, be Backend, subsystem string) (manifest map[string]any, checksum string, snapshotAt string, err error) {
	if err := requireEntities(subsystem); err != nil {
		return nil, "", "", err
	}
	return snapshotEntities(ctx, be, subsystem)
}

// snapshotEntities is ExportSnapshot over objstore.ReadConsistent: it detects
// exactly the writers that carry objstore.Bump, a discipline rather than an
// engine property (BUGS L264). The manifest is rebuilt from nothing on every
// attempt, because the read may run more than once.
func snapshotEntities(ctx context.Context, be Backend, subsystem string) (map[string]any, string, string, error) {
	if be.Obj == nil {
		return nil, "", "", fmt.Errorf("resreg: %s is entity-backed and the backend carries "+
			"no object store", subsystem)
	}
	var manifest map[string]any
	snapshotAt := time.Now().UTC().Format(time.RFC3339)
	err := objstore.ReadConsistent(ctx, be.Obj, store.OwnerScope(subsystem), func(ctx context.Context) error {
		m, err := exportRows(be.read(), subsystem)
		if err != nil {
			return err
		}
		manifest = m
		return nil
	})
	if err != nil {
		return nil, "", "", err
	}
	checksum, err := hashManifest(manifest)
	if err != nil {
		return nil, "", "", err
	}
	return manifest, checksum, snapshotAt, nil
}

// hashManifest is the content hash of one exported document: the one renderer
// Checksum and the snapshot share.
func hashManifest(manifest map[string]any) (string, error) {
	b, err := EmitYAML(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
