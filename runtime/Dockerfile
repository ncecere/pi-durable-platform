# Agent runtime image: one Pi Durable agent per container. Runs as UID 10001
# with a read-only root filesystem (/tmp is a writable emptyDir).
# Multi-arch (linux/amd64, linux/arm64): each stage runs on the target
# platform, so pnpm installs the target's native packages (esbuild).
FROM node:22.23-bookworm-slim AS deps
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --prod

FROM node:22.23-bookworm-slim
# The runtime needs node only: Debian's security updates are applied (the base
# image lags them) and npm, corepack and yarn are removed, so the release scan
# (fixable HIGH and CRITICAL findings fail it) doesn't fail on tools the agent
# never runs.
RUN apt-get update \
    && apt-get upgrade -y --no-install-recommends \
    && rm -rf /var/lib/apt/lists/* \
    && rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack /opt/yarn-* \
    && rm -f /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack /usr/local/bin/yarn /usr/local/bin/yarnpkg
ENV NODE_ENV=production HOST=0.0.0.0 PORT=4100 TSX_TSCONFIG_PATH=/app/tsconfig.json
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY package.json tsconfig.json ./
COPY src ./src
# The release version (the tag without its "v") and commit, as labels: the
# runtime itself doesn't print a version (package.json's "version" is the
# release's base version; the release workflow checks both).
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="pi-durable-platform-runtime" \
      org.opencontainers.image.description="Pi Durable Platform agent runtime: one Pi Durable agent per container" \
      org.opencontainers.image.source="https://github.com/ncecere/pi-durable-platform" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
USER 10001:10001
EXPOSE 4100
CMD ["node", "--import", "tsx", "src/main.ts"]
