-- Scrub the auth.db DSN out of historical audit_log rows.
--
-- authd's daemon.start row (main.go) recorded its own SQLite DSN in
-- params_summary. It was never key material — the other two fields are len()
-- counts — but it is a host filesystem path, it is operator-controlled through
-- the DATABASE env var, and a DSN is the canonical place a credential hides.
-- audit.redactRE now matches `dsn` so no daemon can write one again, and the
-- path an operator actually wants stays where it belongs: the daemon.start
-- slog line, in journald.
--
-- Rows already written predate that fix, and authd is about to serve them at
-- GET /v1/audit (specs/5/I, BUGS F29). Publishing a column means auditing what
-- is already in it, so this removes the one field rather than the row: the
-- counts and every other column survive, so "authd restarted at T serving 2
-- keys" is still answerable.
--
-- daemon.start is the ONLY authd emitter that ever set params_summary — the
-- login row (oauth.go) sets none — so this WHERE reaches every affected row.
UPDATE audit_log
   SET params_summary = json_remove(params_summary, '$.dsn')
 WHERE action = 'daemon.start'
   AND params_summary IS NOT NULL
   AND json_valid(params_summary)
   AND json_extract(params_summary, '$.dsn') IS NOT NULL;
