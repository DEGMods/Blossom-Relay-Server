# Build a static single binary, then ship it on a minimal image.
FROM golang:1.25-alpine AS build
WORKDIR /src
# Cache module downloads separately from the source.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Pure-Go deps (badger/minio/go-nostr) → fully static, no libc needed.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/degnode ./cmd/degnode

FROM alpine:3.20
# TLS roots for R2/S3 + relay connections; run as a non-root user.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 degnode
USER degnode
WORKDIR /app
COPY --from=build /out/degnode /usr/local/bin/degnode
# Identity key, badger event store, ad metrics, and (disk backend) blobs persist here.
VOLUME ["/app/data"]
EXPOSE 3000
# Mount your config at /app/config.yml (see config.example.yml). Secrets are best
# passed as env (R2_ACCESS_KEY/R2_SECRET_KEY or S3_ACCESS_KEY/S3_SECRET_KEY).
ENTRYPOINT ["degnode", "-config", "/app/config.yml"]
