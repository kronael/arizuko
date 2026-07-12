-- route-token link context (spec 5/W § link context): a token optionally
-- carries issuer-authored instructions on how to process the data received
-- through its URL. NULL = pre-context token, behavior unchanged.
ALTER TABLE route_tokens ADD COLUMN context TEXT;
