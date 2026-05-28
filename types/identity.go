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
// Spec: specs/5/U-genericization.md § "types/ — top-level shared IDs".
package types

// UserSub is an OAuth subject: the stable per-user identifier minted by
// the identity provider (Google sub, GitHub user id, ...).
type UserSub string

// Folder is an arizuko folder/tenant identifier, path-structured
// ("krons", "atlas/support"). The opaque cross-boundary shape; folder
// hierarchy semantics live in core/groupfolder.
type Folder string

// Tier is a coarse access level (0 = root … 5+ = thread). Legacy, being
// superseded by capability Scopes — see specs/5/U § "Capability vs tier".
type Tier int

// Scope is a single ACL scope expression ("messages:*:own_group"). A
// capability set is a []Scope.
type Scope string
