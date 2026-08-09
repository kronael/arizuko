-- token_ref and token_expires went inert with the 5/31 pairing fold: the link
-- is minted into route_tokens by promptUnprompted, and handleTokenLanding /
-- jidForToken / claimByToken were deleted with it. Nothing has written either
-- column since, so token_ref is permanently NULL and token_expires is
-- permanently empty — while OnboardingRow still advertised token_expires to
-- REST, MCP, OpenAPI and `arizuko export` as a live "when does the link
-- expire" answer it could never give (BUGS F40).
--
-- Dropped on the 0003 precedent from runed (mcp_tokens + spawns.mcp_token_jti):
-- an always-empty column is not free, it is what a schema reader believes.
-- The index goes first — SQLite refuses DROP COLUMN on an indexed column.
--
-- The legacy carry-forward that filled token_ref is retired with it; what
-- remains of that Go step (store.CarryOnboardingLegacy) only moves the
-- non-credential columns across and drops onboarding_legacy.
DROP INDEX IF EXISTS idx_onboarding_token_ref;
ALTER TABLE onboarding DROP COLUMN token_ref;
ALTER TABLE onboarding DROP COLUMN token_expires;
