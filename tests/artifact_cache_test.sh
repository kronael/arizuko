#!/usr/bin/env bash
set -Eeuo pipefail

fixture=$(mktemp -d)
cleanup() {
  for dir in "$fixture"/data/images/* \
    "$fixture"/data/images/.*-partial.* "$fixture"/fixtures/*
  do
    [[ -d "$dir" ]] || continue
    rm -f -- "$dir/bzImage" "$dir/initrd.gz" "$dir/kernel.config" \
      "$dir/usr/local/bin/node" "$dir/app/entrypoint.sh" "$dir/.image-env.json"
    for leaf in usr/local/bin usr/local usr app
    do
      if [[ -d "$dir/$leaf" ]]
      then
        rmdir -- "$dir/$leaf"
      fi
    done
    rmdir -- "$dir"
  done
  rm -f -- "$fixture/docker" "$fixture/calls" "$fixture/output"
  rmdir -- "$fixture/fixtures" "$fixture/data/images" "$fixture/data" "$fixture"
}
trap cleanup EXIT
mkdir -p "$fixture/fixtures/kernel" "$fixture/fixtures/rootfs/usr/local/bin" \
  "$fixture/fixtures/rootfs/app" "$fixture/fixtures/rootfs-partial/usr/local/bin" \
  "$fixture/data/images"
printf 'kernel\n' > "$fixture/fixtures/kernel/bzImage"
printf 'CONFIG_TEST=y\n' > "$fixture/fixtures/kernel/kernel.config"
printf 'initrd\n' | gzip > "$fixture/fixtures/kernel/initrd.gz"
printf 'node\n' > "$fixture/fixtures/rootfs/usr/local/bin/node"
printf 'entrypoint\n' > "$fixture/fixtures/rootfs/app/entrypoint.sh"
printf 'node\n' > "$fixture/fixtures/rootfs-partial/usr/local/bin/node"

cat > "$fixture/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >> "$FAKE_CALLS"
case "$1" in
  image)
    if [[ "$*" == *'.Config.Env'* ]]
    then
      printf '["A=B"]\n'
    elif [[ ${FAKE_MODE:-} == image_move ]]
    then
      printf 'sha256:other\n'
    else
      printf 'sha256:fixture\n'
    fi
    ;;
  build)
    ;;
  run)
    while (($#))
    do
      if [[ "$1" == -v ]]
      then
        dst=${2%:/dst}
        break
      fi
      shift
    done
    if [[ ${FAKE_MODE:-} == kernel_fail ||
      ${FAKE_MODE:-} == kernel_partial_success ]]
    then
      cp "$FAKE_FIXTURES/kernel/bzImage" "$dst/"
      [[ ${FAKE_MODE:-} == kernel_fail ]] && exit 7
      exit 0
    fi
    cp "$FAKE_FIXTURES/kernel/"* "$dst/"
    ;;
  create)
    printf 'fixture-container\n'
    ;;
  export)
    if [[ ${FAKE_MODE:-} == export_fail ]]
    then
      tar -cf - -C "$FAKE_FIXTURES/rootfs" .
      exit 7
    fi
    if [[ ${FAKE_MODE:-} == bad_tar ]]
    then
      printf 'not a tar stream\n'
      exit 0
    fi
    if [[ ${FAKE_MODE:-} == hang ]]
    then
      sleep 30
    fi
    if [[ ${FAKE_MODE:-} == rootfs_partial_success ]]
    then
      tar -cf - -C "$FAKE_FIXTURES/rootfs-partial" .
      exit 0
    fi
    tar -cf - -C "$FAKE_FIXTURES/rootfs" .
    ;;
  rm)
    if [[ ${FAKE_MODE:-} == cleanup_fail ]]
    then
      exit 7
    fi
    ;;
  *)
    printf 'unexpected fake docker call: %s\n' "$*" >&2
    exit 2
    ;;
esac
EOF
chmod +x "$fixture/docker"
export FAKE_FIXTURES="$fixture/fixtures"
export FAKE_CALLS="$fixture/calls"

run_make() {
  : > "$FAKE_CALLS"
  make --no-print-directory -s "$1" DATA_DIR="$fixture/data" \
    KERNEL_DIR="$2" ROOTFS_DIR="$2" KERNEL_KEY=fixture ROOTFS_KEY=fixture \
    DOCKER="$fixture/docker" > "$fixture/output" 2>&1
}

case "${1:-all}" in
  kernel_hit|all)
    dir="$fixture/data/images/kernel-hit"
    mkdir "$dir"
    cp "$fixture/fixtures/kernel/"* "$dir/"
    run_make kernel "$dir"
    if [[ -s "$FAKE_CALLS" ]]
    then
      cat "$FAKE_CALLS"
      exit 1
    fi
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  kernel_partial|all)
    dir="$fixture/data/images/kernel-partial"
    mkdir "$dir"
    cp "$fixture/fixtures/kernel/bzImage" "$dir/"
    if run_make kernel "$dir"
    then
      exit 1
    fi
    [[ ! -s "$FAKE_CALLS" && ! -e "$dir/initrd.gz" ]]
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  kernel_corrupt_initrd|all)
    dir="$fixture/data/images/kernel-corrupt"
    mkdir "$dir"
    cp "$fixture/fixtures/kernel/"* "$dir/"
    printf 'bad gzip\n' > "$dir/initrd.gz"
    if run_make kernel "$dir"
    then
      exit 1
    fi
    [[ ! -s "$FAKE_CALLS" ]]
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  kernel_export_failure|all)
    dir="$fixture/data/images/kernel-failed"
    export FAKE_MODE=kernel_fail
    if run_make kernel "$dir"
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  kernel_partial_export|all)
    dir="$fixture/data/images/kernel-partial-export"
    export FAKE_MODE=kernel_partial_success
    if run_make kernel "$dir"
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  kernel_fresh|all)
    dir="$fixture/data/images/kernel-fresh"
    run_make kernel "$dir"
    [[ -s "$dir/bzImage" && -s "$dir/initrd.gz" && -s "$dir/kernel.config" ]]
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_hit|all)
    dir="$fixture/data/images/rootfs-hit"
    mkdir -p "$dir/usr/local/bin" "$dir/app"
    cp "$fixture/fixtures/rootfs/usr/local/bin/node" "$dir/usr/local/bin/"
    cp "$fixture/fixtures/rootfs/app/entrypoint.sh" "$dir/app/"
    printf '["A=B"]\n' > "$dir/.image-env.json"
    run_make rootfs "$dir"
    if rg -q '^(create|export) ' "$FAKE_CALLS"
    then
      cat "$FAKE_CALLS"
      exit 1
    fi
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_partial|all)
    dir="$fixture/data/images/rootfs-partial"
    mkdir -p "$dir/usr"
    if run_make rootfs "$dir"
    then
      exit 1
    fi
    [[ ! -e "$dir/.image-env.json" ]]
    if rg -q '^(create|export) ' "$FAKE_CALLS"
    then
      exit 1
    fi
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_invalid_metadata|all)
    dir="$fixture/data/images/rootfs-invalid"
    mkdir -p "$dir/usr/local/bin" "$dir/app"
    cp "$fixture/fixtures/rootfs/usr/local/bin/node" "$dir/usr/local/bin/"
    cp "$fixture/fixtures/rootfs/app/entrypoint.sh" "$dir/app/"
    printf '["OTHER=1"]\n' > "$dir/.image-env.json"
    if run_make rootfs "$dir"
    then
      exit 1
    fi
    if rg -q '^(create|export) ' "$FAKE_CALLS"
    then
      exit 1
    fi
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_image_move|all)
    dir="$fixture/data/images/rootfs-image-move"
    export FAKE_MODE=image_move
    if run_make rootfs "$dir"
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    if rg -q '^(create|export) ' "$FAKE_CALLS"
    then
      exit 1
    fi
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_export_failure|all)
    dir="$fixture/data/images/rootfs-failed"
    export FAKE_MODE=export_fail
    if run_make rootfs "$dir"
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    rg -q '^rm -f fixture-container$' "$FAKE_CALLS"
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_tar_failure|all)
    dir="$fixture/data/images/rootfs-bad-tar"
    export FAKE_MODE=bad_tar
    if run_make rootfs "$dir"
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    rg -q '^rm -f fixture-container$' "$FAKE_CALLS"
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_cleanup_failure|all)
    dir="$fixture/data/images/rootfs-bad-cleanup"
    export FAKE_MODE=cleanup_fail
    if run_make rootfs "$dir"
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    rg -q '^rm -f fixture-container$' "$FAKE_CALLS"
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_partial_export|all)
    dir="$fixture/data/images/rootfs-partial-export"
    export FAKE_MODE=rootfs_partial_success
    if run_make rootfs "$dir"
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_fresh|all)
    dir="$fixture/data/images/rootfs-fresh"
    run_make rootfs "$dir"
    [[ -s "$dir/app/entrypoint.sh" && -s "$dir/usr/local/bin/node" &&
      -s "$dir/.image-env.json" ]]
    [[ ${1:-all} != all ]] && exit 0
    ;;&
  rootfs_interrupt|all)
    dir="$fixture/data/images/rootfs-interrupted"
    export FAKE_MODE=hang
    : > "$FAKE_CALLS"
    if timeout -s TERM 2 make --no-print-directory -s rootfs \
      DATA_DIR="$fixture/data" ROOTFS_DIR="$dir" ROOTFS_KEY=fixture \
      DOCKER="$fixture/docker" > "$fixture/output" 2>&1
    then
      exit 1
    fi
    unset FAKE_MODE
    [[ ! -e "$dir" ]]
    rg -q '^export fixture-container$' "$FAKE_CALLS"
    rg -q '^rm -f fixture-container$' "$FAKE_CALLS"
    [[ ${1:-all} != all ]] && exit 0
    ;;
  *)
    printf 'unknown case: %s\n' "${1:-all}" >&2
    exit 2
    ;;
esac
printf 'artifact cache tests passed\n'
