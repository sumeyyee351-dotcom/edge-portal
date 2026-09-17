# --- Build stage -------------------------------------------------------
# Uses the full Go toolchain to compile a static binary. This stage never
# ships in the final image, so image size doesn't matter here.
FROM golang:1.22-alpine AS builder

WORKDIR /src

# Copy go.mod first so dependency downloads are cached separately from
# source changes (faster rebuilds during development).
COPY go.mod ./
RUN go mod download 2>/dev/null || true

COPY main.go ./
COPY templates ./templates

# go mod tidy resolves/pins the paho.mqtt.golang dependency tree and
# writes go.sum; then we build a static (CGO-disabled) binary so the
# runtime image can be as small as plain Alpine.
RUN go mod tidy && \
    CGO_ENABLED=0 GOOS=linux go build -o /out/edge-portal .

# --- Runtime stage -------------------------------------------------------
# Small Alpine base is enough for an embedded device: no build tools,
# just the binary and CA certs (in case the broker is ever reached via
# a TLS/WebSocket URL).
FROM alpine:3.19

RUN apk add --no-cache ca-certificates && \
    adduser -D -H -s /sbin/nologin portal

WORKDIR /app
COPY --from=builder /out/edge-portal ./edge-portal

USER portal
EXPOSE 8080

CMD ["./edge-portal"]
