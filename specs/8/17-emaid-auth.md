---
status: spec
---

# emaid sender authentication + quarantine

Layered DMARC/DKIM/SPF verification + operator-controlled sender allowlist

- verb annotation that lets routing send unverified mail to a quarantine
  group for operator review. Extends the shipped email adapter (spec 1/8).

**Supersedes** the "All email routes to root group" line in spec 1/8 —
sub-group routing (e.g. quarantine) is the whole point of this spec.

**Replaces** the existing fail-open `authResultsPass` at
`emaid/imap.go:391`. That helper substring-matches against the first
64KB of the raw message and returns true on missing header — opposite
of safe default. The new classifier deletes it.

## Problem

The email adapter delivers any IMAP inbox message to the gateway. Spam,
phishing, prompt-injection via subject/body all reach the agent
unchecked. Routes can't distinguish "mail from a known partner" from
"mail from a random sender claiming to be your CFO." An operator running
atlas with a public-ish address is one well-crafted message away from
the agent acting on attacker instructions.

The information needed to triage is already in standard mail headers
(DKIM-Signature, Authentication-Results, From). Mail servers like
Gmail run DMARC checks on every inbound and stamp the result. emaid
just needs to read it, optionally re-verify cryptographically, and
expose the result so routing can act.

## What ships

Three layered checks, each opt-in via env. All implemented in
emaid, no schema change.

### 1. Parse upstream `Authentication-Results`, pinned to trusted authserv-id

Per **RFC 8601 §5**, receivers MUST discard `Authentication-Results`
headers from untrusted hops — an attacker controlling From: can
inject their own A-R into the body or as an extra header, and many
MTAs concatenate. The shipped `authResultsPass` doesn't do this, so
it's a forgery vector.

**Required env:** `EMAIL_TRUSTED_AUTHSERV` — the authserv-id of the
mail server you trust to evaluate DMARC (e.g. `mx.google.com` for
Gmail IMAP, `mail.fastmail.com` for FastMail). emaid considers ONLY
A-R headers whose authserv-id matches; all others are dropped.

When at least one matching A-R exists, emaid records
`dmarc=pass|fail|none` (per RFC) on `chanlib.InboundMsg`. If no
matching A-R is present, the result is `dmarc=missing` — treated
as untrusted.

If `EMAIL_TRUSTED_AUTHSERV` is unset, default behavior is
**fail-closed**: every inbound is `dmarc=missing` (untrusted) until
the operator explicitly pins their trusted authserv. The previous
silent-trust-anything behavior is gone.

Implementation: **`github.com/emersion/go-msgauth/authres`** — small,
focused, production-tested in Mox. NEW dependency (not transitive
from `go-imap` despite both being from the emersion family). MIT
license, actively maintained (v0.7.0 2025-04). Import the `authres`
package only — avoid `go-milter` subpath which pulls extra deps.

### 2. Operator-configured sender allowlist (opt-in)

New env: `EMAIL_TRUSTED_DOMAINS=mycompany.com,partner.com`. Comma-
separated; trims spaces; lowercase ASCII compare. **Empty (default) =
no allowlist active; only the DMARC check matters.** With the
fail-closed default of (1), this means "DMARC pass alone = trusted"
when the env is unset.

emaid parses the From: header via `net/mail.ParseAddress`:

- Multiple From addresses (group syntax, mailbox-list) → `from_trusted=false`
  always. Spoofing surface; treat as untrusted.
- Single mailbox: extract addr-spec domain, lowercase, IDN-normalize
  (Punycode via `golang.org/x/net/idna`), then compare against the
  list.
- Records `from_trusted=true|false` on the inbound.

### 3. Independent DKIM verification (opt-in, expensive)

New env: `EMAIL_VERIFY_DKIM=true`. When set, emaid runs DKIM
verification itself against the mail's `DKIM-Signature` header (DNS
lookup for the public key, RSA/Ed25519 signature check). Use
**`github.com/emersion/go-msgauth/dkim`** — same library family.

This costs DNS lookup latency per mail (~100ms first time, cached after).
Only worth it when the operator doesn't trust the upstream MX's
Authentication-Results — e.g. running their own MX with multiple hops
that may re-sign. Default OFF; the upstream label is enough for
gmail/fastmail IMAP setups.

### 4. Verb annotation drives routing

Combining 1–3:

```
auth_state = if dmarc=pass AND (allowlist empty OR from_trusted): "trusted"
             else:                                                  "untrusted"
```

emaid sets `InboundMsg.Verb`:

- `auth_state == "trusted"` → `verb = "message"` (today's default)
- `auth_state == "untrusted"` → `verb = "untrusted"` (new value)

**Collision with spec 6/J reply-to-bot promotion**: a stranger
replying to a bot message would otherwise be promoted to `verb=mention`
by the gateway. Spec 6/J runs at api/api.go `handleMessage` AFTER the
adapter ships. Resolution: gateway 6/J check is amended — if
`verb=="untrusted"`, do NOT promote to `mention`. The untrusted signal
wins; an attacker reply-to-bot from an unverified address must not
escalate to trigger.

Subject prefixing is OFF by default. Opt-in via
`EMAIL_UNVERIFIED_SUBJECT_PREFIX=true` if the operator wants the
`[UNVERIFIED]` marker in the visible subject (useful for IMAP search
or operator-side filters). When opt-in is OFF, the signal lives only
in `InboundMsg.Verb` + a structured annotation the agent reads via
the existing message-XML attributes. Default-off prevents an attacker
from poisoning operator expectations by sending mail with
`[UNVERIFIED]` in the legitimate subject.

Operator routes filter as today:

```sql
-- mail from your team, fires the agent
INSERT INTO routes (match, target) VALUES
  ('platform=email verb=message', 'atlas');

-- mail from outside the allowlist or failing DMARC, goes to quarantine
INSERT INTO routes (match, target) VALUES
  ('platform=email verb=untrusted', 'atlas/quarantine');
```

No new daemon code on the gateway side — the existing match
expression grammar already supports `verb=untrusted` (any string value
works; routes just compare). The `atlas/quarantine` group is a
regular sub-group with its own CLAUDE.md instructing the agent how
to handle it (summarize, ping operator, await ✅).

### 5. Strict mode (opt-in)

`EMAIL_STRICT_AUTH=true` (already documented in the original 1/8 spec,
not yet wired): emaid DROPS unauthenticated mail entirely instead of
shipping it as `verb=untrusted`. Routes never see it.

Three tiers of operator policy:

| Stance                           | Config                                                                                                  |
| -------------------------------- | ------------------------------------------------------------------------------------------------------- |
| Fail-closed DMARC only (default) | `EMAIL_TRUSTED_AUTHSERV=<your-mx>` — DMARC-pass = trusted, everything else untrusted                    |
| DMARC + domain allowlist         | `EMAIL_TRUSTED_AUTHSERV=<your-mx>` + `EMAIL_TRUSTED_DOMAINS=mycompany.com,partner.com` — both must pass |
| Reject unauthenticated outright  | `EMAIL_TRUSTED_AUTHSERV=<your-mx>` + `EMAIL_STRICT_AUTH=true` — DROP everything except trusted          |

Without `EMAIL_TRUSTED_AUTHSERV` set, EVERY inbound is `verb=untrusted`.
Operator must opt in to ANY trust by pinning the upstream MX. There
is no "trust everything" mode in v0.41.0+.

## Why not just one big "is this trusted" boolean

Operators want different policies per sender:

- "From inside my company → act"
- "From a known vendor → act, but log"
- "From a stranger but DMARC-verified → summarize and ask me"
- "From anywhere unauthenticated → drop"

Verb annotation lets the operator compose this with existing route
fragments instead of inventing new policy primitives.

## Library choice

`github.com/emersion/go-msgauth` covers all three concerns in one
import path:

- `authres` — parses `Authentication-Results` and produces
  structured results
- `dkim` — independent DKIM verification (used by tier 3)
- `dmarc` — DMARC policy evaluation against DNS

Tradeoffs vs alternatives:

- `go-mail` family — bigger, brings transport code we don't need
  (emaid already uses `emersion/go-imap` for IMAP)
- Roll our own — DKIM signature verification is ~500 LOC of
  cryptography + DNS handling we don't want to maintain
- Skip DKIM entirely (Tier 1+2 only) — fine for gmail-IMAP setups
  but rules out self-hosted MX deployments

`go-msgauth` is already a transitive dep of `emersion/go-imap` we use,
so adding it costs nothing in dependency surface. Production-tested in
Mox + maddy.

## Code surface

| File                                  | Change                                                                  | LOC  |
| ------------------------------------- | ----------------------------------------------------------------------- | ---- |
| `emaid/imap.go:391` `authResultsPass` | DELETE (replaced by classifier)                                         | -22  |
| `emaid/auth.go` (new)                 | A-R parser (pinned authserv-id) + From-domain allowlist + verb decision | ~100 |
| `emaid/dkim.go` (new)                 | optional independent DKIM verify                                        | ~40  |
| `emaid/main.go`                       | wire env vars; call `classify(msg)` before deliver                      | ~15  |
| `emaid/auth_test.go` (new)            | 12 cases (see below)                                                    | ~220 |
| `api/api.go`                          | spec 6/J collision: skip mention promotion when verb=="untrusted"       | ~3   |
| `chanlib/chanlib.go`                  | NO change — Verb is already a string                                    |
| `go.mod` / `go.sum`                   | add `github.com/emersion/go-msgauth` (MIT, NEW dep, small)              | 1    |

Net: ~360 LOC including tests; -22 deletion of the unsafe helper.

## Tests required

`emaid/auth_test.go`:

1. `EMAIL_TRUSTED_AUTHSERV=mx.google.com` + A-R with that authserv-id +
   `dmarc=pass` + no allowlist → `verb=message`
2. Same + allowlist matches From → `verb=message`
3. Same + allowlist doesn't match → `verb=untrusted`
4. Same + `dmarc=fail` → `verb=untrusted`
5. No A-R header at all → `dmarc=missing` → `verb=untrusted`
6. **Forgery: attacker-injected `Authentication-Results: evil-authserv;
dmarc=pass` with no pinned-authserv match → `verb=untrusted`**
7. Multiple A-R headers, one matches the pinned authserv with
   `dmarc=fail`, another (untrusted authserv) with `dmarc=pass` →
   pinned one wins → `verb=untrusted`
8. A-R header LINE FOLDING (RFC 5322 §2.2.3) — multi-line A-R parses
   correctly (substring matchers would miss)
9. `EMAIL_TRUSTED_AUTHSERV` unset → every inbound `verb=untrusted`
   (fail-closed default)
10. `EMAIL_STRICT_AUTH=true` + untrusted → message DROPPED (not delivered)
11. From: with display name `attacker@trusted.com` but real addr-spec
    `bob@untrusted.com` → `from_trusted=false`
12. From: with IDN domain (Punycode `xn--example-...`) matched against
    UTF-8 allowlist entry → normalize before compare
13. Multi-mailbox From: → `from_trusted=false` always
14. Reply-to-bot collision: `verb=untrusted` arriving at gateway 6/J
    promotion path → stays `untrusted`, NOT promoted to `mention`
    (test in api/api_test.go)
15. `EMAIL_UNVERIFIED_SUBJECT_PREFIX=false` (default) → subject
    unmodified; `=true` → prefixed `[UNVERIFIED] `

`emaid/dkim_test.go` (only if `EMAIL_VERIFY_DKIM=true` path is implemented):

16. Mail with valid DKIM signature + DNS key in test resolver → `dkim=pass`
17. Mail with tampered body + valid signature → `dkim=fail`
18. Mail with missing DKIM key in DNS → `dkim=tempfail` (treated as `untrusted`)

## What this is NOT

- NOT new MCP tools. emaid is an adapter, agent doesn't query it for
  auth status; the verb annotation IS the signal.
- NOT a quarantine UI in dashd. Quarantine is just a regular sub-group
  the operator creates (`arizuko group add atlas/quarantine`); the
  operator's existing dashd views cover it.
- NOT a sender reputation system. Just structured pass/fail per the
  email standards.
- NOT cross-instance: each emaid runs against ONE mailbox per spec 1/8.
  Multi-mailbox = multiple emaid containers per spec 5/R-multi-account.

## Operator config recipe

```bash
# In <data-dir>/.env:
EMAIL_TRUSTED_AUTHSERV=mx.google.com             # REQUIRED — your upstream MX authserv-id
EMAIL_TRUSTED_DOMAINS=mycompany.com,partner.com  # comma-separated, optional
EMAIL_STRICT_AUTH=false                          # true = drop unauthenticated
EMAIL_VERIFY_DKIM=false                          # true = independent DKIM verify
EMAIL_UNVERIFIED_SUBJECT_PREFIX=false            # true = prefix [UNVERIFIED] in subject

# Add the quarantine route:
mcpc @s tools-call add_route \
  match:='"platform=email verb=untrusted"' target:='"atlas/quarantine"'

# Create the quarantine sub-group:
mcpc @s tools-call register_group folder:='"atlas/quarantine"'

# Edit atlas/quarantine/CLAUDE.md to instruct the agent:
# "you handle quarantined mail; summarize each inbound and ping the
#  operator at <jid>; wait for ✅ before forwarding to atlas proper."
```

## Open questions

1. Should there be a `dashd` quarantine review page (one-click approve)?
   Defer — operator can react in chat today; UI nice-to-have.
2. Should emaid send a `bounce` reply to dropped strict-mode mail?
   Defer — modern senders don't bounce-on-block; spammers ignore
   bounces; legitimate senders see a delivery failure later from their
   side. Silence is fine.
3. Cross-platform extension: same verb-untrusted pattern could apply
   to other adapters with auth signals (e.g. Slack unverified DMs, X
   unverified accounts). Out of scope here; revisit per-adapter.
