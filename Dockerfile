FROM golang:alpine AS build
RUN apk add --no-cache gcc musl-dev sqlite-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /arizuko cmd/arizuko/main.go
RUN CGO_ENABLED=1 go build -o /gated ./gated/
RUN CGO_ENABLED=1 go build -o /timed ./timed/
RUN CGO_ENABLED=0 go build -o /teled ./teled/
RUN CGO_ENABLED=0 go build -o /discd ./discd/
RUN CGO_ENABLED=0 go build -o /onbod ./onbod/
RUN CGO_ENABLED=0 go build -o /dashd ./dashd/
RUN CGO_ENABLED=0 go build -o /proxyd ./proxyd/

FROM alpine:3.20
RUN apk add --no-cache sqlite-libs ca-certificates docker-cli
COPY --from=build /arizuko /usr/local/bin/arizuko
COPY --from=build /gated /usr/local/bin/gated
COPY --from=build /timed /usr/local/bin/timed
COPY --from=build /teled /usr/local/bin/teled
COPY --from=build /discd /usr/local/bin/discd
COPY --from=build /onbod /usr/local/bin/onbod
COPY --from=build /dashd /usr/local/bin/dashd
COPY --from=build /proxyd /usr/local/bin/proxyd
COPY --from=build /src/ant/skills /opt/arizuko/ant/skills
COPY --from=build /src/ant/CLAUDE.md /opt/arizuko/ant/CLAUDE.md
COPY --from=build /src/template/services /opt/arizuko/template/services
WORKDIR /srv/app/home
