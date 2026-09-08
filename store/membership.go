package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/kronael/arizuko/audit"
	"github.com/kronael/arizuko/objstore"
)

// ErrCycle: adding the edge would create a cycle in acl_membership.
var ErrCycle = errors.New("acl_membership: cycle")

// ErrSelfMembership: child == parent.
var ErrSelfMembership = errors.New("acl_membership: self")

// edgeQuerier is the read the cycle guard walks, satisfied by both *sql.DB and
// *sql.Tx: the writers that need the guard hold different handles, and the guard
// has to run inside the transaction that adds the edge.
type edgeQuerier interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// MembershipParents answers which principals sit directly above one — the
// callback objstore.Reachable walks (objstore/reach.go).
//
// It is EXPORTED because the same guard decides four writes, and it was four
// copies of one recursive CTE: this package's two edge writers, routd's
// agent-facing add, and the manifest apply's per-row validation. A walk written
// four times is a walk that can disagree with itself (BUGS L256).
func MembershipParents(q edgeQuerier) objstore.ParentsFunc {
	return func(_ context.Context, node string) ([]string, error) {
		rows, err := q.Query(`SELECT parent FROM acl_membership WHERE child = ?`, node)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var parent string
			if err := rows.Scan(&parent); err != nil {
				return nil, err
			}
			out = append(out, parent)
		}
		return out, rows.Err()
	}
}

// MembershipWouldCycle reports whether the edge (child → parent) would make
// child its own ancestor.
//
// The walk starts at parent and asks whether child is already at or above it,
// which is what the recursive CTE asked. Reachable seeds with the node itself,
// so a self edge answers true here too — the callers still check it first,
// because ErrSelfMembership is a different answer for the operator.
func MembershipWouldCycle(ctx context.Context, q edgeQuerier, child, parent string) (bool, error) {
	return objstore.Reachable(ctx, MembershipParents(q), parent, child)
}

// AddMembership inserts (child → parent) idempotently. Rejects
// self-membership and any edge that would close a cycle.
//
// Cycle test: starting from `parent`, walk upward via acl_membership
// (parent's parents, ...). If `child` appears in the closure, the new
// edge would form a cycle.
func (s *Store) AddMembership(child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	cycles, err := MembershipWouldCycle(context.Background(), tx, child, parent)
	if err != nil {
		return err
	}
	if cycles {
		return ErrCycle
	}

	if _, err := tx.Exec(
		`INSERT OR IGNORE INTO acl_membership (child, parent, added_by, added_at)
		 VALUES (?, ?, ?, ?)`,
		child, parent, addedBy, time.Now().Format(time.RFC3339),
	); err != nil {
		return err
	}
	// addedBy is the grantor recorded in the row itself; it stands in as the
	// actor for callers that never set AsUser/AsCLI — the fallback AddACLRow
	// already uses, so the two acl writers render one identity.
	actor, actorSub, surface := s.auditIdentity()
	if s.auditSub == "" && addedBy != "" {
		actor, actorSub = addedBy, addedBy
	}
	if err := audit.EmitInTx(context.Background(), s.Obj(), tx, audit.Event{
		Category: audit.CategoryAuthZ,
		Action:   "membership.add",
		Actor:    actor,
		ActorSub: actorSub,
		Surface:  surface,
		Resource: "acl_membership/" + child + "/" + parent,
		Outcome:  audit.OutcomeOK,
		ParamsSummary: map[string]any{
			"child":  child,
			"parent": parent,
		},
	}); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveMembership(child, parent string) error {
	actor, actorSub, surface := s.auditIdentity()
	return s.runAudited(func(tx *sql.Tx) (audit.Event, error) {
		_, err := tx.Exec(
			`DELETE FROM acl_membership WHERE child = ? AND parent = ?`,
			child, parent,
		)
		return audit.Event{
			Category: audit.CategoryAuthZ,
			Action:   "membership.remove",
			Actor:    actor,
			ActorSub: actorSub,
			Surface:  surface,
			Resource: "acl_membership/" + child + "/" + parent,
			Outcome:  audit.OutcomeOK,
		}, err
	})
}

// RoleMember is the seeded floor role — the 12 messaging verbs, seeded by
// routd migration 0023. Every group is born a member of it (5/33): power above
// the floor is explicit delegation, never derived from path depth.
const RoleMember = "role:member"

// SeedMemberRoleTx binds a new folder to RoleMember on a caller-owned
// transaction. It is the ONE writer of the create-time edge, because the
// invariant is "a group row implies the member edge" and it used to live in
// only one of four creating writers: the CLI, dashd and onbod each inserted a
// groups row without it, and a folder with no edge expands to nothing, matches
// zero allow rows and comes up with no messaging tools at all (BUGS L119).
// Idempotent — the underlying write is INSERT OR IGNORE.
func SeedMemberRoleTx(tx *sql.Tx, folder string) error {
	return putMembershipTx(tx, "folder:"+folder, RoleMember, "system:4r-member")
}

// PutMembership inserts (child → parent) idempotently WITHOUT emitting an
// audit_log row — the audit-free twin of AddMembership for an edge no operator
// asked for: the role:member seed every group creation writes, and the 4r
// migration backfill. routd.db (which OWNS acl_membership — spec 5/5) HAS had
// audit_log since routd migration 0016, so a missing table is never the reason;
// an operator-driven grant uses the audited AddMembership. Same self/cycle
// rejection.
func (s *Store) PutMembership(child, parent, addedBy string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := putMembershipTx(tx, child, parent, addedBy); err != nil {
		return err
	}
	return tx.Commit()
}

// putMembershipTx is the audit-free edge write on a caller-owned transaction —
// the single implementation PutMembership and pairing redemption both run.
// Redemption cannot call PutMembership: it must consume the token in the SAME
// transaction, and PutMembership opens its own.
func putMembershipTx(tx *sql.Tx, child, parent, addedBy string) error {
	if child == parent {
		return ErrSelfMembership
	}
	cycles, err := MembershipWouldCycle(context.Background(), tx, child, parent)
	if err != nil {
		return err
	}
	if cycles {
		return ErrCycle
	}
	_, err = tx.Exec(
		`INSERT OR IGNORE INTO acl_membership (child, parent, added_by, added_at)
		 VALUES (?, ?, ?, ?)`,
		child, parent, addedBy, time.Now().Format(time.RFC3339),
	)
	return err
}

// Members returns direct children of `parent`.
func (s *Store) Members(parent string) []string {
	rows, err := s.db.Query(
		`SELECT child FROM acl_membership WHERE parent = ? ORDER BY child`, parent)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil {
			out = append(out, v)
		}
	}
	return out
}

// Ancestors returns the transitive closure of parents reachable from
// `child` via acl_membership, excluding `child` itself. Cycle-safe by
// the UNION (recursive CTE deduplicates).
func (s *Store) Ancestors(child string) []string {
	rows, err := s.db.Query(
		`WITH RECURSIVE up(p) AS (
		   SELECT parent FROM acl_membership WHERE child = ?
		   UNION
		   SELECT acl_membership.parent FROM acl_membership
		     JOIN up ON acl_membership.child = up.p
		 )
		 SELECT p FROM up ORDER BY p`,
		child,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err == nil && v != child {
			out = append(out, v)
		}
	}
	return out
}
