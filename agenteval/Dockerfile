# Built from the repo root context (shares arizuko's go.mod):
#   docker build -f agenteval/Dockerfile -t agenteval .
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /agenteval ./agenteval/cmd/agenteval/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /agenteval /usr/local/bin/agenteval
COPY agenteval/cases /cases
WORKDIR /
ENTRYPOINT ["/usr/local/bin/agenteval"]
CMD ["validate", "--cases", "/cases"]
