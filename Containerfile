# The NAS Dockerfile accepts a local binary; this image builds only tracked source paths.
FROM --platform=$BUILDPLATFORM docker.io/library/golang:1.26.8-alpine AS builder
WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/cato ./cmd/cato

FROM docker.io/library/alpine:3.24
RUN apk upgrade --no-cache && apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 cato && adduser -D -u 10001 -G cato cato \
    && mkdir -p /app/data/covers && chown -R cato:cato /app/data
COPY --from=builder /out/cato /usr/local/bin/cato
COPY web/static /app/web/static
WORKDIR /app
ENV CATO_STATIC_DIR=/app/web/static CATO_DB_PATH=/app/data/cato.db CATO_COVER_DIR=/app/data/covers CATO_LISTEN_ADDR=:7080
USER 10001:10001
EXPOSE 7080
HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=6 \
    CMD wget -q -O /dev/null http://127.0.0.1:7080/healthz || exit 1
CMD ["cato"]
