FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . ./
RUN CGO_ENABLED=0 go build -o /out/ttsd ./ttsd

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /out/ttsd /usr/local/bin/ttsd
USER nobody
EXPOSE 8880
ENTRYPOINT ["/usr/local/bin/ttsd"]
