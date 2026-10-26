FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
COPY third_party ./third_party
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /egress ./egress/cmd/egress/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /egress /usr/local/bin/egress
ENTRYPOINT ["/usr/local/bin/egress"]
