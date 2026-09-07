-- The invitations → invites cutover. Its source table is gone (0028) and its
-- destination is an entity, so there is nothing left on either side of the
-- copy.
--
-- `invites` is an objstore entity rather than a table this chain creates:
-- the driver makes it, from the one declaration that sits beside the code
-- writing it (store.EnsureEntities, spec 6/37 step 7). An entity keeps the name
-- of the table it replaces, so a database carrying rows here is moved by
-- `arizuko migrate-onbod` and refuses to open until it has been.
SELECT 1;
