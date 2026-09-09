-- Recreate sessions table with topic column and composite PK.
-- Existing rows migrated with topic=''.
--
-- The prefix-route uniqueness this file also declared is `routes`, an objstore
-- entity rather than a table this chain creates: the driver makes it, from the
-- one declaration that sits beside the code writing it (store.EnsureEntities,
-- spec 6/37 step 7). A rule is addressed by (seq, target, match), so the
-- coordinate IS the uniqueness a second insert of the same rule would have
-- collided with, and there is no index to declare.
--
-- `sessions` is one too, and its coordinate is (group_folder, topic) — the
-- composite key this file reshaped the table to hold. An entity keeps the name
-- of the table it replaces, so a database carrying rows here is moved by
-- `arizuko migrate-hot` and refuses to open until it has been.

ALTER TABLE messages ADD COLUMN topic TEXT NOT NULL DEFAULT '';
