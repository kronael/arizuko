-- v0.39.0: route mode rides on target as a URI fragment.
-- Spec: specs/6/B-route-mode-ingestion.md. A rule whose target ends `#observe`
-- stores inbound without waking the agent; a rule without it triggers.
--
-- `routes` is an objstore entity rather than a table this chain creates: the
-- driver makes it, from the one declaration that sits beside the code writing
-- it (store.EnsureEntities, spec 6/37 step 7), addressed by (seq, target,
-- match). The target is part of that coordinate, so appending `#observe` to a
-- rule a pre-move database left behind is a re-addressing rather than an
-- UPDATE — and splitting a mixed impulse config into one observe rule plus a
-- higher-priority trigger rule per non-zero verb mints new coordinates. Both
-- belong to `arizuko migrate-routd`, which reads the table this chain left
-- behind.

-- messages.is_observed marks inbound messages stored under a folder via an
-- observe-mode route. The surfacing query reads this flag.
ALTER TABLE messages ADD COLUMN is_observed INTEGER NOT NULL DEFAULT 0;
CREATE INDEX idx_messages_observed ON messages(routed_to, is_observed, timestamp);
