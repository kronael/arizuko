# db_utils

SQL migration runner. Single function, keyed by service+version.

## Purpose

Embedded-FS migration runner used by `store` (and reusable by other
daemons that own their own schemas). Tracks applied migrations in a
`migrations` table keyed by `(service, version)`.

## Public API

- `Migrate(db *sql.DB, fsys embed.FS, dir, service string) error`

## Dependencies

None (stdlib only).

## Files

- `db_utils.go`
