# Standalone linkd build. The canonical image is the root arizuko:latest
# which bundles all daemons; this file exists for isolated builds/testing.
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /linkd ./linkd/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget \
    && addgroup -g 1000 node \
    && adduser -D -u 1000 -G node node \
    && mkdir -p /srv/app/home \
    && chown -R 1000:1000 /srv/app/home
COPY --from=build /linkd /usr/local/bin/linkd
USER 1000:1000
WORKDIR /srv/app/home
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- --tries=1 --timeout=3 http://127.0.0.1:8080/health || exit 1
ENTRYPOINT ["linkd"]
