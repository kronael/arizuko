FROM node:22-alpine AS build
WORKDIR /src
COPY whapd/package.json whapd/package-lock.json* ./
RUN npm install
COPY whapd/tsconfig.json ./
COPY whapd/src/ src/
RUN npx tsc

FROM node:22-alpine
RUN apk add --no-cache ca-certificates wget
WORKDIR /app
COPY --from=build /src/package.json /src/package-lock.json* ./
COPY --from=build /src/node_modules node_modules/
COPY --from=build /src/dist dist/
RUN chown -R node:node /app
USER node
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- --tries=1 --timeout=3 http://127.0.0.1:9002/health >/dev/null || exit 1
ENTRYPOINT ["node", "dist/main.js"]
