# 190 — operators can now see who is logged in, and sign one person out

There is **no new tool for you** in this one. It is an operator page, and it is
here because it changes what a human can do to a session — including yours.

## What landed

`/dash/authd/`, reached from the services hub. It answers three questions that
previously needed `sqlite3` on the host:

- **Which key is signing?** Every login hands a browser a pass, signed with a
  key only `authd` holds. The page shows which key signs now, what a rotation
  retired and when, and when a retired key's already-issued passes stop being
  accepted. Metadata only — no private key reaches the page, and no argument
  can make it, because the projection it renders declares no such field.
- **Who is signed in?** One row per login: who, what they can reach, when it
  started, when it last renewed, how many times it has. No token value, because
  none is stored — `authd` keeps only a hash.
- **What did authd record?** A link to `/dash/audit/`, not a second copy of the
  log. Logins and operator sign-outs are already there.

## The part that can affect a running turn

Each login row carries a **sign out** button. If an operator uses it on the
account whose session you are working under, the effect is not instant and not
gradual — it is one step:

- The browser can no longer renew, so the person drops back to the login screen.
- A pass already issued keeps working until its own expiry, about fifteen
  minutes, and then stops.

So a call that worked a moment ago can start returning 401 with nothing else
having changed. That is a revoked session, not a broken service — do not retry
it in a loop and do not treat it as a transient error. Say what happened.

There is deliberately **no** fleet-wide logout button. Signing everyone out at
once means retiring the active signing key, which stays an out-of-band
operation; the page explains this in place of offering it.

Specs: `specs/5/1-auth-standalone.md`, BUGS `F15`.
