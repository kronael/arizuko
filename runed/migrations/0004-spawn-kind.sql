-- spawns.kind (spec 5/8 "Filesystem restore claims the folder's run slot"):
-- 'agent' = a normal turn (container spawn); other kinds (e.g. 'backup')
-- claim the same per-folder run slot for a folder-exclusive job without a
-- container. kind selects the Manager.Run post-claim executor and scopes
-- circuit-breaker accounting to 'agent' — a failed non-agent run must never
-- count against the agent's breaker. Every pre-existing row is an agent run;
-- the DEFAULT covers them.
ALTER TABLE spawns ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent';
