-- mcp_tokens recorded a per-spawn capability token that nothing ever consumed:
-- no token reached the container (container.Input has no token field and
-- runed/docker.go dropped RunSpec.Token), and authd's Downscope forces
-- Sub=parent.Sub, so every row named service:runed rather than the caller.
-- Spec 5/P amended the delivery away on 2026-07-11; the machinery outlived it.
--
-- Dropped with the Broker that filled it. The agent's authority is the
-- SO_PEERCRED-gated socket runed creates at spawn, not a bearer token.
DROP TABLE IF EXISTS mcp_tokens;
ALTER TABLE spawns DROP COLUMN mcp_token_jti;
