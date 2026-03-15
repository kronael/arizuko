FROM golang:alpine AS build
RUN apk add --no-cache gcc musl-dev sqlite-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /arizuko cmd/arizuko/main.go
RUN CGO_ENABLED=1 go build -o /timed services/timed/main.go
RUN CGO_ENABLED=0 go build -o /teled ./services/teled/

FROM alpine:3.20
RUN apk add --no-cache sqlite-libs ca-certificates docker-cli
COPY --from=build /arizuko /usr/local/bin/arizuko
COPY --from=build /timed /usr/local/bin/timed
COPY --from=build /teled /usr/local/bin/teled
WORKDIR /srv/app/home
ENTRYPOINT ["arizuko"]
