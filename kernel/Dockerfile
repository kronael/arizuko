# syntax=docker/dockerfile:1
# The guest kernel arizuko boots per turn (spec 6/41). Built here and NOT in
# ant/Dockerfile: ant/ took 130 commits in 60 days while this changes when its
# pinned version or fragment changes, and `docker builder prune -f` — the disk
# lever of the spec's host budget — would delete a cache this lived in.
FROM debian:12-slim AS build

ARG KVER=6.1.112
ARG FC_REF=v1.10.1

RUN apt-get update && apt-get install -y --no-install-recommends \
    build-essential flex bison bc libelf-dev libssl-dev \
    curl xz-utils ca-certificates

WORKDIR /src
RUN curl -fsSL "https://cdn.kernel.org/pub/linux/kernel/v6.x/linux-${KVER}.tar.xz" \
    | tar -xJ --strip-components=1

# Firecracker's microvm config is the base; arizuko's fragment is the delta.
# Both land in the artifact so its key covers what produced it.
ADD "https://raw.githubusercontent.com/firecracker-microvm/firecracker/${FC_REF}/resources/guest_configs/microvm-kernel-ci-x86_64-6.1.config" /src/.config
COPY arizuko.fragment /src/arizuko.fragment

RUN ./scripts/kconfig/merge_config.sh -m .config arizuko.fragment \
    && make olddefconfig \
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
# /dev and a console node are NOT optional. CONFIG_DEVTMPFS_MOUNT mounts devtmpfs
# only if /dev exists in the initramfs, and without /dev/console the kernel hands
# PID 1 no stdin, stdout or stderr at all -- the guest then boots, runs and powers
# off in total silence. Measured 2026-09-24: an initrd of one binary produced an
# empty console while the same binary printed correctly on the host.
# ttyS0 is here as well as console, and it is the one guest-init writes to. A
# kernel with CONFIG_VT=y routes /dev/console to the virtual terminal, which a
# guest with no video never shows, so the serial node is the only reliable sink.
# Both are real nodes rather than devtmpfs's, so the console works before any
# mount and cannot depend on one.
RUN cd /root && mkdir -p dev \
    && mknod -m 622 dev/console c 5 1 \
    && mknod -m 622 dev/ttyS0 c 4 64 \
    && mknod -m 622 dev/kmsg c 1 11 \
    && printf 'init\ndev\ndev/console\ndev/ttyS0\ndev/kmsg\n' \
       | cpio -o -H newc | gzip -9 > /out/initrd.gz
