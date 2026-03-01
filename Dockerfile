FROM node:22-bookworm-slim AS deps

RUN apt-get update -yy && \
    apt-get install -yy --no-install-recommends \
      ca-certificates python3 make g++ && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /srv/app
COPY package.json package-lock.json ./
RUN npm ci

FROM deps AS build

COPY tsconfig.json ./
COPY src/ ./src/
RUN npx tsc

FROM node:22-bookworm-slim

RUN apt-get update -yy && \
    apt-get install -yy --no-install-recommends \
      ca-certificates curl git docker.io && \
    rm -rf /var/lib/apt/lists/*

WORKDIR /srv/app

COPY --from=build /srv/app/dist ./dist
COPY --from=deps /srv/app/node_modules ./node_modules
COPY --from=deps /srv/app/package.json ./package.json
COPY container/ ./container/
COPY template/ /srv/app/template/

RUN mkdir -p /cfg
COPY kanipi ./kanipi
RUN chmod +x ./kanipi

RUN useradd -m -u 1000 node 2>/dev/null || true

CMD ["./kanipi"]
