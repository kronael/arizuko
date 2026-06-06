---
status: partial
---

# specs/7/E — Encryption at rest

> Why this matters for phase 7: encrypted secrets in SQLite are
> exactly why [`../8/3-git-as-truth.md`](../8/3-git-as-truth.md) keeps
> secret blobs OUT of git. Git carries the `(scope, name)` reference;
> the encrypted value stays here, decrypted in-process at spawn.

**Shipped (secrets table):** AES-256-GCM on `secrets.value` via `Store.SetSecretKey` + `Store.PurgeUnencryptedSecrets`. Key = SHA-256(AUTH_SECRET). Plaintext rows purged on startup — operators must re-enter secrets after first encrypted boot.

**Deferred:** messages.db column encryption (content, raw).

## What this solves

`secrets` table and `messages.db` are plaintext on disk. An attacker
with filesystem access gets Slack tokens, Anthropic API keys, and full
message history. Required for any enterprise deployment.

## Scope

- `secrets` table: envelope-encrypt per-row values with a KMS-backed key
- `.env` at-rest: out of scope (operator concern; document FS-level encryption)
- `messages.db` content columns: encrypt `content`, `raw` at write time
- Key derivation: support local key file (default) and external KMS (AWS KMS / GCP KMS via env var)

## Not in scope

- Key rotation procedure (follow-on spec)
- Search over encrypted content
- Audit log of key access

## Open questions

- Which columns in `messages` need encryption — content + raw only, or attachments too?
- Local key file path convention (`/srv/data/<instance>/keyring`?)
- Whether to use SQLCipher vs application-level column encryption
