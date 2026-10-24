# Freeride Proxy — Standalone k3s Container Image
# Multi-arch compatible (linux/amd64, linux/arm64)
# Deploys ONLY the Freeride proxy binary (no Gas Town components).
FROM --platform=$BUILDPLATFORM golang:1.26.9-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /src
ENV CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH}

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY *.go ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/go/buildcache-freeride \
    GOCACHE=/go/buildcache-freeride \
    go test ./... -tags='!integration' \
    && go build -ldflags="-s -w" -o /out/freeride .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata curl \
    && addgroup -g 1000 app && adduser -D -u 1000 -G app app

WORKDIR /app
COPY --from=builder /out/freeride /usr/local/bin/freeride
COPY k3s/models.yaml /app/models.yaml
RUN chown -R app:app /app

USER app
EXPOSE 11434

ENV PORT=11434

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD curl -fsS http://localhost:11434/api/hello || exit 1

ENTRYPOINT ["/usr/local/bin/freeride"]
