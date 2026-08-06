# 189 — a revoked login stays revoked, and operators can see who is logged in

Two changes on `authd`, the daemon that signs every token in the system. There
is **no new tool for you** in this one — both are below the level you work at.
It is here because one of them changes what happens to a session, and that is
worth knowing when someone tells you they were logged out.

## The one that matters: a revoked session cannot come back

When a refresh token is presented twice — the signal that one of them was
stolen — authd kills the whole rotation lineage. It turned out the kill could
lose a race with the rotation it was killing: the lineage was marked dead, and
a successor token that had been minted a moment later survived it, valid for
another thirty days. The alarm fired and the credential it was about stayed
live.

The claim and the successor now happen in one database transaction, so nothing
can land between them, and the claim also refuses a lineage that has already
been killed. If a session is revoked, it is revoked.

**One visible change**: if the grants service is unreachable during a refresh,
your refresh token is no longer spent. Before, the token was consumed and the
request then failed, so a brief outage logged people out; now the refresh is
refused and the same token still works when the service comes back.

## For operators

Three new endpoints on `authd`, all bearer-and-scope gated:

- `GET /v1/signing_keys` — which key is signing right now, when it was created
  or retired, and when a retired key stops verifying tokens it signed. The JWK
  Set at `/v1/keys` carries none of that, so "did the rotation take" used to
  need `sqlite3` on the box. **Metadata only** — no private key reaches this,
  and no argument can make it.
- `GET /v1/sessions` — one row per login: who, what scope, when it started,
  when it was last renewed, how many times it has rotated, and whether it is
  active, revoked or expired. Never a token value.
- `DELETE /v1/sessions/{family_id}` — end someone's session. This is the
  incident-response verb; `/auth/logout` already covered the case where the
  person does it themselves. The revoke is recorded in authd's audit log, which
  `/dash/audit/` shows.

They are on `/openapi.json` like every other resource. `sessions:read` and
`sessions:write` are separate scopes, so a dashboard that shows the table does
not thereby gain the ability to end a session.

**Not yet wired**: the `/dash/authd/` page. The cockpit tile stays greyed until
it exists, and `service:dashd` holds none of these scopes until it has a page
that needs them.

Specs: `specs/5/1-auth-standalone.md`, BUGS `F36` and `F15`.
