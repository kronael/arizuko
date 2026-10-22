// Package types holds the shared identity types that cross daemon
// boundaries — the IDs that each `<daemon>/api/v1/` package and the
// daemon implementations refer to without dragging in core's
// domain-rich types (and without creating import cycles between every
// api/v1 package and core).
//
// Pure types only: no behavior, no methods, no constants beyond
// zero-values, and zero arizuko-internal imports (stdlib only). Anything
// richer — JID parsing, folder-hierarchy semantics, scope evaluation —
// stays in core/ or in the daemon that owns the semantics. types/ is the
// boundary, not the implementation.
//
// Spec: specs/5/A-primitives-framing.md § "Identity".
package types

// UserSub is an OAuth subject: the stable per-user identifier minted by
// the identity provider (Google sub, GitHub user id, ...).
type UserSub string

// Folder is an arizuko folder/tenant identifier, path-structured
// ("krons", "atlas/support"). The opaque cross-boundary shape; folder
// hierarchy semantics live in core/groupfolder.
type Folder string
