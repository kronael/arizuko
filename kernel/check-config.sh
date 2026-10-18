#!/usr/bin/env bash
set -Eeuo pipefail

fragment=${1:?fragment path required}
resolved=${2:?resolved config path required}
source=${3:?kernel source path required}
assertions=$(awk '/^CONFIG_[A-Z0-9_]+=/ || /^# CONFIG_[A-Z0-9_]+ is not set$/ {print}' "$fragment")
if [[ -z "$assertions" ]]
then
  echo "kernel: fragment has no config assertions" >&2
  exit 1
fi

printf '%s\n' "$assertions" | while IFS= read -r expected
do
  case "$expected" in
    '# CONFIG_'*' is not set')
      symbol=${expected#"# CONFIG_"}
      symbol=${symbol%" is not set"}
      if ! grep -R -E -q --include='Kconfig*' \
        "^[[:space:]]*(menu)?config[[:space:]]+${symbol}([[:space:]]|$)" "$source"
      then
        echo "kernel: CONFIG_$symbol is not declared by Kconfig" >&2
        exit 1
      fi
      if grep -Eq "^CONFIG_${symbol}=" "$resolved"
      then
        echo "kernel: requested CONFIG_$symbol=n, but olddefconfig enabled it" >&2
        exit 1
      fi
      ;;
    *)
      if ! grep -Fxq "$expected" "$resolved"
      then
        echo "kernel: olddefconfig did not keep $expected" >&2
        exit 1
      fi
      ;;
  esac
done
