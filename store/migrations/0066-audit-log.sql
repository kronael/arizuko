-- 0066 — audit_log unified event table
--
-- Source of truth for security events and state changes. Replaces
-- the older split between ipc_audit (MCP mutations) and cli_audit
-- (CLI mutations) with one homogeneous shape covering MCP / REST /
-- CLI / gateway / cron / crackbox / agent surfaces.
--
-- Spec: specs/5/I-tool-call-logging.md (field schema), audit/PLAN.md
-- (master event list).
--
-- Field schema mirrors 5/I's slog keys plus the bookkeeping columns
-- needed for forensic queries: scope (vs folder for multi-level
-- resources), request_id (correlation across daemons), source_ip
-- (REST surface). outcome is a closed three-value enum:
--   ok      — call ran successfully and the mutation committed
--   error   — call failed for non-authz reasons
--   denied  — authz refused the call (carved out from error so
--             forensic queries on AccessDenied don't drown in
--             everything-else-that-broke).
--
-- Indexes target the queries actually run by ops + adversary:
--   created_at — "what happened in the last 24 h"
--   actor_sub  — "what did Alice do" (partial — most rows have a sub)
--   folder     — "what happened in atlas/support"
--   (cat,act)  — "show me every secret.set" or "every authz.deny"
--
-- Round-2 of the audit comprehensive-coverage plan. Round-3 may add
-- a (folder, created_at) composite if folder-windowed queries get
-- slow at >1M rows. Retention is open question per 5/I Q3.
CREATE TABLE IF NOT EXISTS audit_log (
  id              INTEGER PRIMARY KEY AUTOINCREMENT,
  created_at      TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
  category        TEXT    NOT NULL,
  action          TEXT    NOT NULL,
  actor           TEXT    NOT NULL,
  actor_sub       TEXT,
  resource        TEXT,
  scope           TEXT,
  surface         TEXT,
  params_summary  TEXT,
  outcome         TEXT    NOT NULL,
  error_msg       TEXT,
  duration_ms     INTEGER,
  turn_id         TEXT,
  folder          TEXT,
  instance        TEXT,
  request_id      TEXT,
  source_ip       TEXT
);

CREATE INDEX IF NOT EXISTS audit_log_created_at ON audit_log(created_at);
CREATE INDEX IF NOT EXISTS audit_log_actor_sub  ON audit_log(actor_sub) WHERE actor_sub IS NOT NULL;
CREATE INDEX IF NOT EXISTS audit_log_folder     ON audit_log(folder)    WHERE folder    IS NOT NULL;
CREATE INDEX IF NOT EXISTS audit_log_cat_act    ON audit_log(category, action);

-- Consolidate ipc_audit + cli_audit into audit_log. Both tables are
-- recent (0061 / 0062, May 2026) with low row counts and no external
-- consumers beyond the audit JSONL poll. Round-2 of the audit
-- comprehensive-coverage plan keeps the legacy tables in place but
-- stops writing to them; round-3 will drop the tables once every
-- writer is on audit.EmitInTx and the JSONL poll cursor is migrated.
