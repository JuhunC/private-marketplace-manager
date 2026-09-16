# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.1-alpine AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w -X github.com/JuhunC/private-marketplace-manager/internal/buildinfo.Version=$VERSION" -o /manager ./cmd/manager
FROM alpine:3.23
RUN apk add --no-cache ca-certificates && addgroup -g 10001 manager && adduser -D -H -u 10001 -G manager manager && mkdir -p /data/extensions /data/state && chown -R 10001:10001 /data
COPY --from=build /manager /usr/local/bin/manager
USER 10001:10001
ENV LISTEN_ADDR=:8080 EXTENSIONS_DIR=/data/extensions STATE_DIR=/data/state
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=6s --start-period=60s CMD ["/usr/local/bin/manager", "--healthcheck"]
ENTRYPOINT ["/usr/local/bin/manager"]
