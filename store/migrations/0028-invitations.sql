-- `invitations` was the pre-0032 invite table and 0032 was its only consumer.
-- Both are gone with the move: an invite is an objstore item, minted by
-- store.CreateInvite at partition store.OwnerOnbod.
--
-- `invites` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An entity keeps the name
-- of the table it replaces, so a database carrying rows here is moved by
-- `arizuko migrate-onbod` and refuses to open until it has been.
SELECT 1;
