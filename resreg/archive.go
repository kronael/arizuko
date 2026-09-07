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
	"database/sql"
	"encoding/hex"
	"fmt"
	"time"
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
func ExportSnapshot(ctx context.Context, db *sql.DB, subsystem string) (manifest map[string]any, checksum string, snapshotAt string, err error) {
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, "", "", fmt.Errorf("begin read tx: %w", err)
	}
	defer tx.Rollback()
	snapshotAt = time.Now().UTC().Format(time.RFC3339)
	manifest, err = Export(tx, subsystem)
	if err != nil {
		return nil, "", "", err
	}
	b, err := EmitYAML(manifest)
	if err != nil {
		return nil, "", "", err
	}
	sum := sha256.Sum256(b)
	checksum = "sha256:" + hex.EncodeToString(sum[:])
	return manifest, checksum, snapshotAt, nil
}
