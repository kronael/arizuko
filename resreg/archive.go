package resreg

// The archive INDEX — spec 5/8 "The full-instance archive". An archive is a
// superset containing the config manifest, and this file holds the part of it
// that belongs to no single table: archive.yaml's shape, the consistency level
// it declares, and the one-read-transaction subsystem snapshot the checksum is
// stamped from. It reuses Export/EmitYAML/Checksum for the config half
// verbatim; nothing here forks that renderer.
//
// The per-table halves are NOT here. An archive carries values no manifest
// does — messages' whole history, route_tokens' verifier, invites' ref, the
// onboarding verdicts, secrets' blob — and each of those is a second reader of
// ONE table, so each lives as a method on that table's owner beside its first
// reader (store/messages.go, route_tokens.go, invites.go, onboarding.go,
// secrets.go). A generic engine that also held five tables' column lists was
// the owner of tables it knows nothing else about.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/kronael/arizuko/objstore"
	"github.com/kronael/arizuko/store"
)

// ArchiveFormatVersion is archive.yaml's own format version — a small
// integer bumped only when an archive document's shape changes, independent
// of arizuko's release tags (spec 5/8 "Cross-instance portability").
// `archive apply` refuses outright when an archive's stamped format_version
// exceeds this.
const ArchiveFormatVersion = 1

// Consistency levels an archive.yaml declares (spec 5/8 "Consistency
// levels"). live = per-subsystem read-tx snapshots, no downtime, the
// archive as a whole is a smear across wall-clock time. quiesced = the
// operator already stopped the instance before exporting; the flag only
// stamps the metadata, this package does not stop/start anything.
const (
	ConsistencyLive     = "live"
	ConsistencyQuiesced = "quiesced"
)

// ArchiveManifest is archive.yaml: the archive's top-level index. It
// declares which consistency level produced it and, per subsystem, the read
// transaction's start timestamp — never a claim of one cross-archive
// point-in-time image (spec 5/8 "Consistency levels").
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

// ExportSnapshot exports one subsystem's manifest-visible projection inside
// ONE explicit read transaction, closing the gap spec 5/8 "Consistency
// levels" names: Export's several ScanAll calls were each their own
// implicit-autocommit read; wrapping them in one read-only tx gives the
// whole subsystem document a single WAL MVCC snapshot instead of one per
// resource. Returns the same manifest map Export always has, plus the
// content-hash checksum (the same value Checksum computes, without a
// second Export pass) and the snapshot's start timestamp.
func ExportSnapshot(ctx context.Context, be Backend, subsystem string) (manifest map[string]any, checksum string, snapshotAt string, err error) {
	if err := requireEntities(subsystem); err != nil {
		return nil, "", "", err
	}
	return snapshotEntities(ctx, be, subsystem)
}

// snapshotEntities is ExportSnapshot for a subsystem whose resources are
// entities: objstore.ReadConsistent in place of the read transaction, because
// there is no snapshot across N reads to open (objstore/consistent.go).
//
// It reads the subsystem's version marker, runs the same Export, and reads the
// marker again; a marker that moved means a writer committed during the read and
// the export runs again. What it detects is exactly the writers that carry
// objstore.Bump, which is a discipline rather than an engine property (BUGS
// L264) — a bounded writer that forgets it is invisible here, the same silent
// way a missing audit row is.
//
// The manifest is rebuilt from nothing on every attempt, because the read may
// run more than once and a retry that appended to the first attempt's map would
// answer a document neither state ever had.
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

// hashManifest is the content hash of one exported document — the same value
// Checksum computes, without a second Export pass. One renderer, so the two
// paths above cannot produce checksums that disagree with each other or with
// Checksum.
func hashManifest(manifest map[string]any) (string, error) {
	b, err := EmitYAML(manifest)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
