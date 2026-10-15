# Deployment direction

The current installation runs separate daemons. Use the published installation
docs for commands that work today.

The target single-world composition runs the core modules in one binary and
process. One operator can own many groups and agents. Module ownership,
authorization, and isolated agent execution remain explicit.

Persistence uses the Dynamo-compatible API. Packages supply capabilities and
skills. Package import adapters translate other package formats into the same
contract and lifecycle. A package does not require its own process.

The functional minimum is writable workspace files, messaging with delegation,
and discoverable skills. This target composition is not an available install mode.
