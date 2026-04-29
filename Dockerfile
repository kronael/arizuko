# Reproducibility contract: base images pinned to explicit tags.
# Bump versions intentionally; digests can be re-pinned via `docker pull` output.
FROM golang:1.25-alpine AS build
RUN apk add --no-cache gcc musl-dev sqlite-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /arizuko ./cmd/arizuko/
RUN CGO_ENABLED=1 go build -o /gated ./gated/
RUN CGO_ENABLED=1 go build -o /timed ./timed/
RUN CGO_ENABLED=0 go build -o /teled ./teled/
RUN CGO_ENABLED=0 go build -o /discd ./discd/
RUN CGO_ENABLED=0 go build -o /onbod ./onbod/
RUN CGO_ENABLED=0 go build -o /dashd ./dashd/
RUN CGO_ENABLED=0 go build -o /proxyd ./proxyd/
RUN CGO_ENABLED=0 go build -o /webd ./webd/
RUN CGO_ENABLED=0 go build -o /linkd ./linkd/

FROM alpine:3.20
RUN apk add --no-cache sqlite-libs ca-certificates docker-cli wget \
    && addgroup -g 1000 node \
    && adduser -D -u 1000 -G node node \
    && mkdir -p /srv/app/home \
    && chown -R 1000:1000 /srv/app/home
COPY --from=build /arizuko /usr/local/bin/arizuko
COPY --from=build /gated /usr/local/bin/gated
COPY --from=build /timed /usr/local/bin/timed
COPY --from=build /teled /usr/local/bin/teled
COPY --from=build /discd /usr/local/bin/discd
COPY --from=build /onbod /usr/local/bin/onbod
COPY --from=build /dashd /usr/local/bin/dashd
COPY --from=build /proxyd /usr/local/bin/proxyd
COPY --from=build /webd /usr/local/bin/webd
COPY --from=build /linkd /usr/local/bin/linkd
COPY --from=build /src/ant/skills /opt/arizuko/ant/skills
COPY --from=build /src/ant/CLAUDE.md /opt/arizuko/ant/CLAUDE.md
COPY --from=build /src/template/services /opt/arizuko/template/services
WORKDIR /srv/app/home
# Each daemon exposes /health on :8080 internally. HEALTHCHECK probes it;
# compose uses `depends_on: service_healthy` to gate startup.
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD wget -qO- --tries=1 --timeout=3 http://127.0.0.1:8080/health || exit 1
