FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY cato /usr/local/bin/cato
COPY web/static /app/web/static
RUN mkdir -p /app/data/covers
ENV CATO_STATIC_DIR=/app/web/static
ENV CATO_DB_PATH=/app/data/cato.db
ENV CATO_COVER_DIR=/app/data/covers
EXPOSE 7080
CMD ["cato"]
