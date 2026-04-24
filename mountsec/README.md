# mountsec

Mount allowlist + inbound path validation for container security.

## Purpose

Guards Docker mounts and MCP-tool-supplied paths. `ValidateAdditionalMounts`
checks group-configured mounts against a caller-supplied `Allowlist`.
`ValidateFilePath` resolves symlinks, rejects escapes, and blocks
known-sensitive patterns (`.ssh`, `.gnupg`, `.env`, private keys).
Container path for extra mounts: `/workspace/extra/<name>`.

## Public API

- `ValidateAdditionalMounts(...) ([]ValidMount, error)`
- `ValidateFilePath(path, root string) (string, error)`
- `AllowedRoot`, `Allowlist`, `AdditionalMount`, `ValidMount`

## Dependencies

None (stdlib only).

## Files

- `mountsec.go`

## Related docs

- `ARCHITECTURE.md` (Mount Security)
- `SECURITY.md`
