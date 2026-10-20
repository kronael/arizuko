# mountsec

Mount allowlist + inbound path validation for container security.

## Purpose

Guards guest mounts and MCP-tool-supplied paths. `ValidateAdditionalMounts`
checks group-configured mounts against a caller-supplied `Allowlist`. It
applies `Allowlist.BlockedPatterns` only when the caller fills them, and
resolves no symlink (BUGS `L496`).
`ValidateFilePath` resolves symlinks, rejects escapes, and blocks
known-sensitive patterns (`.ssh`, `.gnupg`, `.env`, private keys).
Container path for extra mounts: `/mnt/<name>`.

## Public API

- `ValidateAdditionalMounts(mounts, groupName, isRoot, al) []ValidMount` — a
  rejected mount is logged and left out of the result
- `ValidateFilePath(path, root string) (string, error)`
- `AllowedRoot`, `Allowlist`, `AdditionalMount`, `ValidMount`

## Dependencies

None (stdlib only).

## Files

- `mountsec.go`

## Related docs

- `runed/README.md` § Mounts
- `SECURITY.md`
