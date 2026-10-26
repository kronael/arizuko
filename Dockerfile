# syntax=docker/dockerfile:1
# Reproducibility contract: base images pinned to explicit tags.
# Bump versions intentionally; digests can be re-pinned via `docker pull` output.
FROM golang:1.27.3-alpine AS build
RUN apk add --no-cache gcc musl-dev sqlite-dev make
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download
COPY . .
RUN --mount=type=cache,target=/root/.cache/go-build \
    make -j$(nproc) docker-build OUT=/

FROM alpine:3.20
# The VMM side (spec 6/41 §"The runed deploy topology"): qemu runs one guest per
# turn, virtiofsd exports the turn's share, socat bridges the guest's MCP port,
# iptables writes the tap's rule set — the one that accepts the guest's proxy and
# drops everything else — and iproute2 creates the tap itself. BOTH were missing
# (measured 2026-09-25 against the image). iproute2 is the one that matters most:
# BusyBox `ip` is on the PATH and has no `tuntap` subcommand, so a presence check
# passes and the guest gets no interface (BUGS `L369`).
# Alpine 3.20's virtiofsd is the RUST daemon (1.10.1), which is the one that can
# clamp the guest's uid — QEMU's C daemon cannot, and dropping the Rust BUILD the
# spec planned for is measured, not assumed. docker-cli stays: no turn uses it any
# more, but the `arizuko` CLI this image also ships drives `docker compose` for
# run, pair and status.
RUN apk add --no-cache sqlite-libs ca-certificates docker-cli wget \
    qemu-system-x86_64 virtiofsd socat iptables iproute2 \
    && addgroup -g 1000 node \
    && adduser -D -u 1000 -G node node \
    && mkdir -p /srv/app/home \
    && chown -R 1000:1000 /srv/app/home
COPY --from=build /arizuko /usr/local/bin/arizuko
COPY --from=build /egress /usr/local/bin/egress
COPY --from=build /authd /usr/local/bin/authd
COPY --from=build /routd /usr/local/bin/routd
COPY --from=build /runed /usr/local/bin/runed
COPY --from=build /timed /usr/local/bin/timed
COPY --from=build /teled /usr/local/bin/teled
COPY --from=build /discd /usr/local/bin/discd
COPY --from=build /onbod /usr/local/bin/onbod
COPY --from=build /dashd /usr/local/bin/dashd
COPY --from=build /proxyd /usr/local/bin/proxyd
COPY --from=build /webd /usr/local/bin/webd
COPY --from=build /slakd /usr/local/bin/slakd
COPY --from=build /gitd /usr/local/bin/gitd
COPY --from=build /emaid /usr/local/bin/emaid
# /opt/arizuko is the DEFAULT source runed and routd read `ant/` from: skills,
# CLAUDE.md and the per-surface output-styles all ship in the image, so a
# release is a build and not whatever sits in a checkout on the host.
COPY --from=build /src/ant/skills /opt/arizuko/ant/skills
COPY --from=build /src/ant/CLAUDE.md /opt/arizuko/ant/CLAUDE.md
COPY --from=build /src/ant/output-styles /opt/arizuko/ant/output-styles
COPY --from=build /src/template/services /opt/arizuko/template/services
# The migrate skill's announce step reads the release notes from
# /opt/arizuko/CHANGELOG.md inside the agent container. It is here because
# the agent's /opt/arizuko is a copy of THIS path (MaterializeAppSrc), and
# omitting it made every announce a no-op (BUGS L89).
COPY --from=build /src/CHANGELOG.md /opt/arizuko/CHANGELOG.md
WORKDIR /srv/app/home
# Each daemon exposes /health on :8080 internally. HEALTHCHECK probes it;
# compose uses `depends_on: service_healthy` to gate startup.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- --tries=1 --timeout=3 http://127.0.0.1:8080/health || exit 1
