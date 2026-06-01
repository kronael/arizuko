-- network_rules: the per-folder egress allowlist routd resolves at dispatch and
-- passes to runed (RunRequest.EgressAllowlist) so the spawned container is
-- attached to the crackbox network with the right allowed targets. Ported from
-- store/migrations/0037 (gated owned it pre-split; routd owns it post-split, per
-- the resreg resource catalog). folder='' is the instance-wide base allowlist
-- inherited by every folder (folderAncestry).
CREATE TABLE network_rules (
  folder      TEXT NOT NULL,
  target      TEXT NOT NULL,
  created_at  TEXT NOT NULL,
  created_by  TEXT NOT NULL DEFAULT '',
  PRIMARY KEY (folder, target)
);

CREATE INDEX idx_network_rules_folder ON network_rules(folder);

INSERT INTO network_rules (folder, target, created_at, created_by) VALUES
  ('', 'anthropic.com',     CURRENT_TIMESTAMP, 'system'),
  ('', 'api.anthropic.com', CURRENT_TIMESTAMP, 'system');
