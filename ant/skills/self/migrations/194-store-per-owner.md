# 194 — each daemon's database moved into a directory of its own

Nothing changes for you. No new tool, no changed tool, no file in your home
moved. This note exists because the instance you run in was restructured
underneath, and an operator reading your migration log should be able to see
when.

## What landed

Every daemon's SQLite database used to sit side by side in one directory:

```
store/routd.db   store/auth.db   store/onbod.db   store/runed.db
```

Each now lives in a directory named after the daemon that owns it:

```
store/routd/routd.db   store/authd/auth.db
store/onbod/onbod.db   store/runed/runed.db
```

## Why it was worth doing

The directory is what a container mount can be pointed at. With everything in
one directory, any daemon granted its own database was granted all of them —
"this table belongs to routd" was a rule people followed, not a rule the system
enforced. Now `webd` and `proxyd` are handed `store/routd/` and literally
cannot see `auth.db`; `authd` is handed `store/authd/` and cannot see anything
else.

`store/messages.db` — the retired pre-split file nothing opens — stays where it
is. It has no owner, so it gets no directory.

## What an operator has to do

Nothing. `arizuko generate` moves the files, and the systemd unit already runs
it on every restart, after the containers stop and before they start. Running
it twice is a no-op.

Spec: `specs/5/16-mcp-rest-unification.md` step 7.
