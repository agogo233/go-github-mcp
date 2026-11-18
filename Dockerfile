# syntax=docker/dockerfile:1

FROM golang:1.25-alpine AS builder
ARG VERSION=dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/go-github-mcp ./cmd/server

FROM alpine:3.20
RUN addgroup -g 10001 app && adduser -D -u 10001 -G app app
COPY --from=builder /out/go-github-mcp /usr/local/bin/go-github-mcp
USER 10001:10001
EXPOSE 8080
# Unauthenticated liveness probe served by the process itself.
HEALTHCHECK --interval=30s --timeout=3s --start-period=5s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/go-github-mcp"]
