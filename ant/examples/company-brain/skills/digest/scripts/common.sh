#!/usr/bin/env bash
# shellcheck disable=SC2034  # REPO, CHANNELS: read by scripts that source or parse this file
# Shared settings for the digest scripts. Source it: . "$(dirname "$0")/common.sh"
# Every setting comes from the group config, `~/digest.toml` (scripts/config.py).
set -Eeuo pipefail
HOME="${HOME:-/home/node}"
export HOME

DIGEST_ROOT="${DIGEST_ROOT:-$HOME/tmp/digest}"
CFG="$(dirname "${BASH_SOURCE[0]}")/config.py"
REPO="$("$CFG" repo)"
CHANNELS="$("$CFG" channels)"

# mcp_open <session>: connect an mcpc session to the arizuko socket
mcp_open() {
  local cfg="$DIGEST_ROOT/arizuko.mcp.json"
  local sock="${ARIZUKO_MCP_SOCKET:-/run/ipc/gated.sock}"
  mkdir -p "$DIGEST_ROOT"
  if [[ ! -f "$cfg" ]]
  then
    printf '{"mcpServers":{"arizuko":{"command":"socat","args":["UNIX-CONNECT:%s","-"]}}}\n' \
      "$sock" > "$cfg"
  fi
  mcpc connect "$cfg:arizuko" "@$1" >/dev/null
}

# mcp_close <session>: close it; a session that is already gone is fine
mcp_close() {
  mcpc "@$1" close >/dev/null 2>&1 || true
}

# mcp_call <session> <tool> key:=json ... : print .content[0].text
mcp_call() {
  local session="$1"
  local tool="$2"
  shift 2
  mcpc --json "@$session" tools-call "$tool" "$@" | jq -r '.content[0].text'
}
