# routd

The `routd` daemon binary.

## What is tested here

The env readers (`envOr`, `intOr`, `durOr`, `parseCSV`) and `keysetVerifier`:
every one of them must fail toward the documented default, and the verifier
must return NO identity alongside an error rather than a partial subject a
handler might trust.

`main` is not, and cannot be, unit-tested in this package. It loads a real
config, opens the owner DB, fetches authd's JWKS over the network, and (for
`runed`) requires a reachable container runtime — each of those calls
`os.Exit(1)` on failure, which takes the test binary with it. That path is
covered end to end by `make test-e2e` and by `tests/`.

Read the coverage number with that in mind: `main` is most of the statements
in this package, so the percentage measures how much of the binary's
_bootstrap_ a unit test can reach, not how much of its behaviour is checked.
