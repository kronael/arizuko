FROM golang:1.27.3-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/mcpfw ./mcpfw/cmd/mcpfw

FROM alpine:3.21
COPY --from=build /out/mcpfw /usr/local/bin/mcpfw
ENTRYPOINT ["mcpfw"]
