-- routd owns the Slack agent pane sessions (spec 6/D, spec 5/5 § Daemon
-- ownership), and they are an objstore entity rather than a table this chain
-- creates: the driver makes it, from the one declaration that sits beside the
-- code writing it (store.EnsureEntities, spec 6/37 step 7). An entity keeps the
-- name of the table it replaces, so a routd.db carrying rows here is moved by
-- `arizuko migrate-store` and refuses to open until it has been.
SELECT 1;
