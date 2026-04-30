# davd: WebDAV daemon. sigoden/dufs is distroless (no shell, no wget),
# so we wrap it in alpine to add a healthcheck binary. Same convention
# as every other arizuko daemon — /health probe, listens on :8080.
FROM sigoden/dufs:latest AS dufs
FROM alpine:3.20
RUN apk add --no-cache wget ca-certificates
COPY --from=dufs /bin/dufs /usr/local/bin/dufs
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- --tries=1 --timeout=3 http://127.0.0.1:8080/ >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/dufs"]
