# Agent runtime image: one Pi Durable agent per container. Runs as UID 10001
# with a read-only root filesystem (/tmp is a writable emptyDir).
FROM node:22.23-bookworm-slim AS deps
WORKDIR /app
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile --prod

FROM node:22.23-bookworm-slim
ENV NODE_ENV=production HOST=0.0.0.0 PORT=4100 TSX_TSCONFIG_PATH=/app/tsconfig.json
WORKDIR /app
COPY --from=deps /app/node_modules ./node_modules
COPY package.json tsconfig.json ./
COPY src ./src
USER 10001:10001
EXPOSE 4100
CMD ["node", "--import", "tsx", "src/main.ts"]
