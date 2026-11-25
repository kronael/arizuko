---
status: shipped
---

# specs/4 — dashboards, memory, scheduler, personas

No spec is open in this phase. Agent memory is in `diary/README.md`; the
authorization model is in `auth/README.md`.

Merged or moved out of this phase:

| Was                     | Now                                                                                                                                                                                                                                                                        |
| ----------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `9-gated.md`            | Deleted — `gated` was removed at v0.50.0. Read [`../5/E-routd.md`](../5/E-routd.md) (routing + store + loop), [`../5/P-runed.md`](../5/P-runed.md) (queue + containers + MCP socket), [`../5/1-auth-standalone.md`](../5/1-auth-standalone.md) (OAuth, ES256 signer, JWKs) |
| `18-web-vhosts.md`      | [`../5/V-web-vhosts.md`](../5/V-web-vhosts.md) (2026-05-26)                                                                                                                                                                                                                |
| `15-code-research.md`   | [Product authoring](../../EXTENDING.md#installing-packages)                                                                                                                                                                                                                |
| `1-channel-protocol.md` | [`../5/34-channel-protocol.md`](../5/34-channel-protocol.md) (2026-08-02)                                                                                                                                                                                                  |
| `9-acl-unified.md`      | [`../5/32-acl-unified.md`](../5/32-acl-unified.md) (2026-08-02)                                                                                                                                                                                                            |
| `R-paths-roles.md`      | [`../5/33-paths-roles.md`](../5/33-paths-roles.md) (2026-08-02)                                                                                                                                                                                                            |
| `Y-minimal-setup.md`    | [`../5/27-compose-native-packaging.md`](../5/27-compose-native-packaging.md)                                                                                                                                                                                               |
| `26-prototypes.md`      | Deleted — the `prototype/` spawn, its `max_children` cap and the derived folder naming were removed 2026-08-04. Groups are created explicitly with `register_group`; delegating to a folder that does not exist fails with an error naming that tool.                      |
