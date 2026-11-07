---
status: shipped
depends:
  [
    ../5/32-acl-unified,
    ../5/33-paths-roles,
    ../5/19-hitl-firewall,
    ../5/31-identity-pairing,
    26-personal-mode,
    40-store-surface,
  ]
---

# ACL layers use one evaluator and one grant check

Access control reads local rules before the release defaults.
`auth/defaults.go` declares the defaults. Operators and agents write local
rules in `acl`. Both layers use `core.ACLRow`.

## Local rules must override defaults without changing their format

A release must supply its defaults to an existing instance without writing
operator data. Local rules must survive a restart and decide each matching
call. A rule mutation must use the same policy state for its authorization
check and its write.

## Where it fits

```text
acl_membership ─► principal expansion ─► local acl rows
                                              │
auth/defaults.go ─► default rows               │
                         │                    │
                         ▼                    ▼
                 ReadPolicy then Policy.Allows
                         │
        ┌────────────────┼──────────────────┐
        ▼                ▼                  ▼
    Authorize        CheckHold         ShellEnvKeys

rule mutation ─► CheckGrant ─► policy version ─► proved write
```

`auth/README.md` owns the evaluator and containment rules.
`store/acl.go` owns the shared invite and world grant builders.
`store/writeset.go` and `store/scopes.go` carry policy versions into writes.

## A rule, and the two layers it comes from

The layer is not a row field. `granted_by` records the writer and grants no
authority. Removing a local rule exposes the remaining policy, including the
current defaults. `store.SeedRoutdFloor` writes only network rules.

## One evaluator, and the layer it reads

`Policy.Allows` uses matching local rows when any match the request.
Otherwise it uses matching defaults. Deny wins within the selected layer.
No matching allow means refusal.

`ReadPolicy` reads memberships and local rules through
`objstore.ReadConsistent` on routd's scope. A failed read refuses access.
`CheckHold` returns the error to `holdGate`, which blocks the call.
A scope replacement also refuses policy reads while the scope is held.

Hold and shell actions accept exact actions or their own namespace wildcard.
Other action grants do not cover these namespaces. `EffectiveActions` and
`UserScopes` list allows from both layers. A listing does not replace a
per-target verdict.

## The defaults are one file, read top to bottom

`auth/defaults.go` is the only default-policy declaration. Role membership
selects the applicable defaults. A world's hold policy comes from its
`role:world` edge, so local rows can override individual default skips.
A stored blanket hold selects the local layer for every matching hold.

## CheckGrant — one gate for delegated rule mutations, in one read

`auth.CheckGrant` reads the writer's policy and the affected principals in
one consistent read. It answers the door's ACL question before checking the
mutation. The returned version binds the write to that policy state.

ACL add and remove handlers, unpair, dashboard grants and approval resolution
use this check. Invite issuance checks the proposed grant without writing it.
Redemption and invited world creation check again before their rule writes.
Host CLI and manifest writes, consent pairing and folder bootstrap use their
own authority.

### The containment tests

`auth/grant.go` selects the local layer when a local action and scope may
overlap the wanted authority. Otherwise it selects defaults. A deny in that
layer refuses an authority check. A covering allow must have no predicate.
Its scope must cover the request. Its params must be empty or identical.
Sender-only allows cannot supply a turn's grant authority. Sender denies
still apply.

| Mutation                                     | Required authority                                              |
| -------------------------------------------- | --------------------------------------------------------------- |
| Add an allow or remove a row outside `hold:` | Covering authority with the grant option                        |
| Add a deny outside `hold:`                   | The door's authorization check                                  |
| Add or remove a `hold:` row                  | `admin` on its scope, without the grant option                  |
| Add an edge                                  | Authority for the parent's allows and hold rules in both layers |
| Remove an edge                               | Authority for the parent's denies, hold rules and local allows  |

An added allow cannot name a wildcard principal. A removal still checks a
wildcard principal. Removing a local allow requires the authority to add that
allow with the option, because the removal can expose a default's authority.
An operator's `*` can delegate shell access with the option even though that
row supplies no shell access at execution.

Scope containment is conservative. A wanted glob needs an identical held
glob, `**`, or a covering recursive prefix. A deny that might intersect the
requested scope blocks the grant. The check does not infer safety from a
deny's params or predicate.

### The proof

`WriteSet.Prove`, `AddACLRow`, `RemoveACLRow` and `SeedMemberRole` carry the
checked version. Their batches use `objstore.BumpAt` on routd's marker.
A moved version refuses the whole batch with `ErrScopeMoved`.
REST maps that error to 409. Other routd writes also move this marker, so
unrelated activity can require a person to retry a grant.

### Who the granter is

REST and dashboard mutations use the verified sub. Approval resolution uses
the reviewer. `turnGranter` supplies the route and sender for an ordinary
agent turn. An elevated turn uses only the `/root` issuer.

The operator shortcut tests the raw row with `store.OperatorShape`:
`**`, omitted action, empty params and predicate, and an allow effect.
An explicit `admin` or `*` is an ordinary ACL row.

### A purge runs only from the host CLI

`arizuko group rm` is the purge path. A purge can remove denies that affect
other folders. Dashboard deletion therefore returns 409 and names the host
command. Registration rollback deletes only its group row.

## Invitations, worlds and unpairing are grant writes

`store.InviteGrantRow` and `store.WorldGrantRows` render the rows that the
issuer must be able to grant. Invite creation records the checked issuer.
onbod checks that issuer before consuming the invite and before creating an
invited world. `agent:<folder>` resolves to `folder:<folder>`.
The issuers `cli`, `onbod` and `routd` use trusted authority without that check.

A refused redemption check leaves the invite unspent. A refused grant write
attempts `RestoreInvite`. A failed restore leaves the invite spent and logs
an error. A subgroup redemption records its issuer and confers no ACL row
until world creation. A redemption with no issuer cannot create a world.
Invited creation retries a moved policy version up to `createWorldAttempts`.
A revoke after the redemption is spent can leave no world and a spent redemption.
Approved first-world admission uses its own creation authority.

Unpair checks an edge removal. A parent deny or a local allow without the
required grant authority can prevent self-unpair. An operator can perform the
authorized agent operation. REST also requires the caller to be the parent.

## An operator is an evaluator verdict

routd's `IsOperator` asks for `admin` on `**`. `UserScopes` includes `**` only
when the same policy allows that question. `/root` checks this verdict once
for its turn. Ordinary elevated tool gates bypass per-target policy checks.
Rule mutations still evaluate the issuer through `CheckGrant`.

## dashd pages ask the evaluator

`callerScope` reads one policy per request. `visible` asks for `interact` on
the folder for pages, rows and counts, including for operators.
`requireOperator` asks for `admin` on `**`. `adminOn` uses the verified sub
without scope strings as extra principals.

## `/always` writes only a concrete rule

`holdShapes` declares the keys that a standing rule may pin. A tool with no
keys offers no Always button and refuses `/always`. `pinnedSpec` also refuses
missing scalar values and values that cannot form an exact matcher.
The notice and writer use the same rendered rule. Approval resolution checks
the reviewer and writes the verdict and standing row with one policy version.

## The live rows after the deploy

Stored copies of defaults remain local rows and select the local layer.
The operator must audit each instance before removing exact copies with
`arizuko group ungrant`. Local exceptions stay.

## Acceptance

The behavior checks are in `auth/layers_test.go`, `auth/grant_test.go`,
`auth/userscopes_test.go`, `store/chat_seed_test.go`,
`routd/hold_read_failure_test.go`, `routd/acl_resource_test.go`,
`routd/acl_rest_delegate_test.go`, `routd/unpair_test.go`,
`routd/root_batch_test.go`, `routd/hold_world_inversion_test.go`,
`routd/hold_always_test.go`, `onbod/split_test.go`,
`dashd/grants_action_test.go`, `dashd/isolation_test.go` and
`dashd/group_delete_purge_test.go`.

## The text that states the rule

The lasting rules are in `GRANTS.md`, `auth/README.md`,
`specs/5/32-acl-unified.md`, `specs/5/33-paths-roles.md`,
`specs/5/31-identity-pairing.md` and `specs/6/26-personal-mode.md`.
The operator reference is
`template/web/pub/arizuko/reference/grants.html`.
`cmd/arizuko/README.md`, `onbod/README.md`, `routd/README.md` and
`dashd/README.md` state their gates. `CHANGELOG.md` records the release change.
The `invite_create` description in `ipc/ipc.go` states the issuer's grant requirement.
The source checks are in `routd/membership_resource.go`, `onbod/main.go`,
`store/seed.go`, `dashd/authz.go` and `routd/hold_world_inversion_test.go`.
