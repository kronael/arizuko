-- route_tokens.kind (spec 5/31): 'route' = the /chat/ + /hook/ delivery bearer,
-- 'pair' = an identity-pairing link. Resolution is kind-scoped in BOTH
-- directions — route delivery accepts only 'route', pairing redemption only
-- 'pair' — so neither can be redeemed as the other. Pairing is a kind of route
-- token, not a table of its own, which puts the token and the acl_membership
-- edge it writes in ONE database and therefore ONE transaction.
-- Every pre-existing row is a route token; the DEFAULT covers them.
ALTER TABLE route_tokens ADD COLUMN kind TEXT NOT NULL DEFAULT 'route';
