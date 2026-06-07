# Built from the repo root context (shares arizuko's go.mod):
#   docker build -f anteval/Dockerfile -t anteval .
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /anteval ./anteval/cmd/anteval/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /anteval /usr/local/bin/anteval
COPY anteval/cases /cases
WORKDIR /
ENTRYPOINT ["/usr/local/bin/anteval"]
CMD ["validate", "--cases", "/cases"]
