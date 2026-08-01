package routd

// seed_grants.go carries the 4/R default-role assignment: every group is born a
// member of role:member (the messaging-verb floor, seeded by migration 0023).
// Power above the floor is explicit delegation from a lineage ancestor or the
// operator's root grant — never derived from path depth.

import "github.com/kronael/arizuko/store"

// RoleMember is the seeded floor role (the 12 messaging verbs). Migration 0023
// seeds its acl rows; every folder principal is bound to it at group creation.
const RoleMember = "role:member"

// assignDefaultRole binds a folder principal to role:member (idempotent —
// PutMembership is INSERT OR IGNORE). Called at group creation so a folder holds
// the messaging floor as DATA the moment it exists.
func assignDefaultRole(st *store.Store, folder string) error {
	return st.PutMembership("folder:"+folder, RoleMember, "system:4r-member")
}
