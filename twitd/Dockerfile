FROM oven/bun:1-alpine AS build
WORKDIR /src
COPY twitd/package.json ./
RUN bun install
COPY twitd/tsconfig.json ./
COPY twitd/src/ src/
RUN bunx tsc

FROM oven/bun:1-alpine
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=build /src/package.json ./
COPY --from=build /src/node_modules node_modules/
COPY --from=build /src/dist dist/
USER bun
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- --tries=1 --timeout=3 http://127.0.0.1:8080/health >/dev/null || exit 1
ENTRYPOINT ["bun", "dist/main.js"]
