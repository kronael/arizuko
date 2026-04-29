FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /crackbox ./crackbox/cmd/crackbox/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates wget
COPY --from=build /crackbox /usr/local/bin/crackbox
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
    CMD wget -qO- --tries=1 --timeout=3 http://127.0.0.1:3129/health >/dev/null || exit 1
ENTRYPOINT ["/usr/local/bin/crackbox"]
CMD ["proxy", "serve"]
