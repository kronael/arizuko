---
status: partial
---

# specs/8/E — Encryption at rest

`secrets` and `messages.db` sit plaintext on disk. An attacker with
filesystem access reads Slack tokens, Anthropic keys, and the whole message
history. Encrypting the secret values is the part enterprise review always
asks for first.

## Shipped — the `secrets` table

AES-256-GCM over `secrets.value`, stored as `"v2:" + base64(nonce||ciphertext)`
([`store/secrets.go`](../../store/secrets.go) — `secretV2Prefix`, `seal`,
`gcm`). A value without the prefix is legacy plaintext and is read as-is, so
an un-keyed instance keeps working.

The key comes from `SECRETS_KEY`, not from `AUTH_SECRET`
([`core/config.go`](../../core/config.go) — `SecretKeyring`). It is a
**keyring**: comma-separated, first non-empty entry seals, the rest decrypt
only. That is the rotation mechanism — add the new key in front, let the old
one linger until `EncryptPlaintextSecrets` has rewritten every row.

`Store.EncryptPlaintextSecrets` migrates legacy rows in place and is
idempotent. It replaced `PurgeUnencryptedSecrets`, which **DELETEd** plaintext
rows on startup — and because the write path never wrote the `v2:` prefix it
tested for, that purge wiped every secret on every boot. The lesson is in the
shape: a migration that removes data on a predicate the writer doesn't
maintain is a data-loss bug, not a cleanup.

Related: redeploying an image to an instance whose `.env` lacks `SECRETS_KEY`
crash-looped a daemon and cost a krons outage (2026-06-05). Set the key before
the first keyed boot.

## Deferred — `messages.db`

Content-column encryption (`content`, `raw`) is not built. It is a heavier
call than the secrets table because it trades away SQL search over message
bodies, and the open questions are still open:

- Which columns actually need it — `content` + `raw`, or attachments too?
- SQLCipher (whole-file, transparent) vs application-level column encryption?

## Not in scope

`.env` at rest is an operator concern — document filesystem-level encryption
instead. Search over encrypted content and an audit log of key access are
separate specs.
