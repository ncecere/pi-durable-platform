# syntax=docker/dockerfile:1
#
# Pi Durable Platform image: the Go server with the web UI embedded.
# The agent runtime has its own image (runtime/Dockerfile).
#
# The web UI and the Go binary are built on the build platform and
# cross-compiled for the target (no emulation for the slow steps).

FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --ignore-scripts
COPY web/ ./
# The type-check covers src/test, which reads ../api/openapi.yaml.
COPY api/ /api/
RUN npm run build && test -f dist/index.html

FROM --platform=$BUILDPLATFORM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
COPY --from=web /web/dist ./web/dist
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath \
    -ldflags "-s -w -X github.com/ncecere/pi-durable-platform/internal/buildinfo.Version=${VERSION} -X github.com/ncecere/pi-durable-platform/internal/buildinfo.Commit=${COMMIT}" \
    -o /out/pi-durable-platform ./cmd/pi-durable-platform

# Distroless: no shell or package manager; runs as a non-root user (65532)
# with a read-only root filesystem. Kubernetes probes /healthz and /readyz.
FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="pi-durable-platform" \
      org.opencontainers.image.description="Pi Durable Platform: deploy, test and manage Pi Durable agents" \
      org.opencontainers.image.source="https://github.com/ncecere/pi-durable-platform" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
COPY --from=build /out/pi-durable-platform /pi-durable-platform
USER 65532:65532
EXPOSE 8098
ENTRYPOINT ["/pi-durable-platform"]
CMD ["serve"]
