FROM golang:alpine AS build
RUN apk add --no-cache gcc musl-dev sqlite-dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o /arizuko cmd/arizuko/main.go

FROM alpine:3.20
RUN apk add --no-cache sqlite-libs ca-certificates docker-cli
COPY --from=build /arizuko /usr/local/bin/arizuko
WORKDIR /srv/app/home
ENTRYPOINT ["arizuko"]
