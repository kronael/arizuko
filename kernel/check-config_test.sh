#!/usr/bin/env bash
set -Eeuo pipefail

fixture=$(mktemp -d)
cleanup() {
  rm -f -- "$fixture/Kconfig" "$fixture/fragment" "$fixture/resolved" "$fixture/output"
  rmdir -- "$fixture"
}
trap cleanup EXIT
cat > "$fixture/Kconfig" <<'EOF'
config EXPERT
  bool
config VT
  bool
config VGA_CONSOLE
  bool
EOF
cat > "$fixture/fragment" <<'EOF'
CONFIG_EXPERT=y
# CONFIG_VT is not set
# CONFIG_VGA_CONSOLE is not set
EOF
cat > "$fixture/resolved" <<'EOF'
CONFIG_EXPERT=y
CONFIG_VT=y
CONFIG_VGA_CONSOLE=y
EOF

if kernel/check-config.sh "$fixture/fragment" "$fixture/resolved" "$fixture" \
  > "$fixture/output" 2>&1
then
  echo 'kernel config checker accepted a requested VT=n as VT=y' >&2
  exit 1
fi
grep -Fq 'requested CONFIG_VT=n' "$fixture/output"

cat > "$fixture/resolved" <<'EOF'
# CONFIG_EXPERT is not set
# CONFIG_VT is not set
EOF
if kernel/check-config.sh "$fixture/fragment" "$fixture/resolved" "$fixture" \
  > "$fixture/output" 2>&1
then
  echo 'kernel config checker accepted a missing EXPERT=y' >&2
  exit 1
fi
grep -Fq 'did not keep CONFIG_EXPERT=y' "$fixture/output"

cat > "$fixture/resolved" <<'EOF'
CONFIG_EXPERT=y
# CONFIG_VT is not set
EOF
kernel/check-config.sh "$fixture/fragment" "$fixture/resolved" "$fixture"

printf '# CONFIG_MISSING is not set\n' > "$fixture/fragment"
if kernel/check-config.sh "$fixture/fragment" "$fixture/resolved" "$fixture" \
  > "$fixture/output" 2>&1
then
  echo 'kernel config checker accepted an unknown disabled symbol' >&2
  exit 1
fi
grep -Fq 'not declared by Kconfig' "$fixture/output"
echo 'kernel config assertions passed'
