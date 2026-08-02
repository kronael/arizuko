---
status: shipped
---

# Worlds

A world is the first segment of a folder path: `worldOf("atlas/support")
== "atlas"`. `auth.WorldOf` / `auth.isInWorld` (`auth/identity.go`),
`router.IsAuthorizedRoutingTarget` (`router/router.go:269`).

The world is the delegation boundary. Same world and a descendant is
allowed; cross-world, sibling, ancestor, and same-folder are denied.
Inter-world traffic goes through `delegate_group` / `escalate_group`,
never a direct send — that keeps one auditable hop between tenants
instead of an implicit mesh.

**Depth no longer confers authority.** The old model derived a tier from
segment count and let it open tool slots; `5/33` dissolved it, and
`auth.Resolve` now returns a folder that "carries ZERO authorization —
only its own name". A folder's capability comes from its `acl` rows.
World membership survives as a _containment_ rule, which is a different
thing: it says who you may address, not what you may do.

## Share mount

`groups/<world>/share` mounts at `/var/lib/share`. Whether it is
writable is a container-capability grant (`ShareReadOnly`), resolved by
routd at dispatch — not a function of depth.
