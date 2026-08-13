-- route_tokens.target_folder — the folder the token DELIVERS to (spec 5/W).
-- owner_folder bounds revocation and may differ from the delivery target, so it
-- cannot answer "where does this payload go".
--
-- Before this column the answer came from the JID grammar alone, which is
-- ambiguous as soon as the mint adds a suffix: `web:t/sfx` is byte-identical to
-- a no-suffix mint at folder `t/sfx`, and `hook:a/b/c` is both (folder `a/b`,
-- source `c`) and (folder `a`, source `b`, suffix `c`). routd resolved the wrong
-- folder, found no group, and onbod parked the payload in the admission queue
-- (BUGS L4). The mint knows the target; it now records it.
--
-- NULL means a row minted before this column. Those rows keep the grammar
-- (groupfolder.JidFolder), which is what they were minted under, so a live
-- instance does not break on the upgrade.

ALTER TABLE route_tokens ADD COLUMN target_folder TEXT;
