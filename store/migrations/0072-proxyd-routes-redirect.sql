-- Allow a proxyd_routes row to be a redirect (no backend).
-- redirect_to non-empty → 302 to destination; backend must be ''.
ALTER TABLE proxyd_routes ADD COLUMN redirect_to TEXT NOT NULL DEFAULT '';
