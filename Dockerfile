FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY cato /usr/local/bin/cato
COPY web/static /app/web/static
RUN mkdir -p /app/data/covers
ENV CATO_STATIC_DIR=/app/web/static
ENV CATO_DB_PATH=/app/data/cato.db
ENV CATO_COVER_DIR=/app/data/covers
EXPOSE 7080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -q -O /dev/null http://127.0.0.1:7080/healthz || exit 1
CMD ["cato"]
