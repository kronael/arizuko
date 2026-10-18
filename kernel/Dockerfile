# syntax=docker/dockerfile:1
# The guest kernel arizuko boots per turn (spec 6/41). Built here and NOT in
# ant/Dockerfile: ant/ took 130 commits in 60 days while this changes when its
# pinned version or fragment changes, and `docker builder prune -f` — the disk
# lever of the spec's host budget — would delete a cache this lived in.
FROM debian:12-slim AS build

ARG KVER=6.1.112
ARG FC_REF=v1.10.1

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential flex bison bc libelf-dev libssl-dev lz4 \
    curl xz-utils ca-certificates

WORKDIR /src
RUN curl -fsSL "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KVER}.tar.xz" \
    | tar -xJ --strip-components=1

# Firecracker's microvm config is the base; arizuko's fragment is the delta.
# Both land in the artifact so its key covers what produced it.
ADD "https://raw.githubusercontent.com/firecracker-microvm/firecracker/${FC_REF}/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config" /src/.config
COPY arizuko.fragment /src/arizuko.fragment
COPY check-config.sh /src/check-config.sh

RUN ./scripts/kconfig/merge_config.sh -m .config arizuko.fragment \
    && make olddefconfig \
    && ./check-config.sh arizuko.fragment .config /src \
    && make -j"$(nproc)" bzImage

# The guest's whole userspace: one static binary as /init.
FROM golang:1.25-bookworm AS init
WORKDIR /g
COPY guest-init/ .
RUN CGO_ENABLED=0 go build -ldflags '-s -w' -o /init .

FROM debian:12-slim AS out
RUN apt-get update && apt-get install -y --no-install-recommends cpio gzip
COPY --from=build /src/arch/x86/boot/bzImage /out/bzImage
COPY --from=build /src/.config /out/kernel.config
COPY --from=init /init /root/init
# /dev and a console node are NOT optional, and the kernel will NOT populate /dev
# for us: CONFIG_DEVTMPFS_MOUNT is set and has no effect on an initramfs boot -- the
# kernel calls devtmpfs_mount() only when it mounts a real root. Measured
# 2026-09-25: the guest reached its mounts and then failed `start /usr/bin/socat:
# open /dev/null: no such file or directory`, because /dev held exactly the three
# nodes below and nothing else (BUGS `L374`). guest-init cannot fix that by mounting
# devtmpfs itself either -- doing so invalidates the console fd the kernel handed
# PID 1, which cost a whole debug cycle on 2026-09-24. So every node the turn needs
# is a REAL node in the initrd, and /newroot/dev is a rec-bind of it.
#
# Without /dev/console the kernel hands
# PID 1 no stdin, stdout or stderr at all -- the guest then boots, runs and powers
# off in total silence. Measured 2026-09-24: an initrd of one binary produced an
# empty console while the same binary printed correctly on the host.
# ttyS0 is here as well as console, and it is the one guest-init writes to. A
# kernel with CONFIG_VT=y routes /dev/console to the virtual terminal, which a
# guest with no video never shows, so the serial node is the only reliable sink.
# Both are real nodes rather than devtmpfs's, so the console works before any
# mount and cannot depend on one.
RUN cd /root && mkdir -p dev dev/pts \
    && mknod -m 622 dev/console c 5 1 \
    && mknod -m 622 dev/ttyS0 c 4 64 \
    && mknod -m 622 dev/kmsg c 1 11 \
    && mknod -m 666 dev/null c 1 3 \
    && mknod -m 666 dev/zero c 1 5 \
    && mknod -m 666 dev/full c 1 7 \
    && mknod -m 666 dev/random c 1 8 \
    && mknod -m 666 dev/urandom c 1 9 \
    && mknod -m 666 dev/tty c 5 0 \
    && mknod -m 666 dev/ptmx c 5 2 \
    && printf 'init\ndev\ndev/console\ndev/ttyS0\ndev/kmsg\ndev/null\ndev/zero\ndev/full\ndev/random\ndev/urandom\ndev/tty\ndev/ptmx\ndev/pts\n' \
       | cpio -o -H newc | gzip -9 > /out/initrd.gz
