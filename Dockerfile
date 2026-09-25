# The Sparkjudge hosted API (cmd/sparkjudge-api). The configs are embedded in
# the binary; secrets arrive as environment variables (`fly secrets set`).
# docs/sparkjudge-api.md walks through a deploy.

FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sparkjudge-api ./cmd/sparkjudge-api

# Alpine rather than distroless: a Fly volume mounts owned by root, so the
# entrypoint hands /data to the app user once (as root) and then drops to it
# with su-exec. Nothing else runs as root.
FROM alpine:3.22
RUN apk add --no-cache ca-certificates su-exec \
 && adduser -D -H -u 10001 sparkjudge
COPY --from=build /out/sparkjudge-api /usr/local/bin/sparkjudge-api
COPY deploy/sparkjudge/entrypoint.sh /usr/local/bin/entrypoint
ENV SPARKJUDGE_DATA_DIR=/data SPARKJUDGE_LISTEN=:8080
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/entrypoint"]
