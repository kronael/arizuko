---
status: shipped
---

# Worlds

World = first folder segment of a group path. Enforced by
`IsAuthorizedRoutingTarget` in `gateway/`.

```
worldOf('atlas/support') === 'atlas'
```

## Authorization boundary

- Root world groups (`root`, `root/*`) can delegate to any folder
  in any world
- Same world, descendant: allowed
- Cross world: denied
- Sibling, ancestor, same-folder: denied

## Share mount pattern

```
/workspace/share <- groups/<world>/share
```

Tier 0 and world groups can write. Deeper groups are readonly.

## Open questions

- Wildcard JID registration
- Hierarchical platform JIDs like `telegram:chat:thread`
- Tree-wide IPC auth beyond current action checks
