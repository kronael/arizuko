# 033 — Verifier audit trail

The `/facts` skill now writes verification records to `verifier/`.

Each verified fact gets a corresponding `verifier/<fact>.md` with YAML
frontmatter: `result` (pass/fail), `verified_at`, `reason`.

- On pass: `verified_at` updated in both `facts/` and `verifier/`
- On fail: fact deleted from `facts/`, record kept in `verifier/`

This provides an audit trail — you can check `verifier/` to see what
was verified and what was rejected.

No action needed. Next `/facts` run will create `verifier/` records
automatically.
