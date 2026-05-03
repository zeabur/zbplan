# keywords: oven javascript typescript nodejs
# description: Bun app: oven/bun builder with install cache, bun-alpine runtime
FROM oven/bun:1-alpine AS builder
WORKDIR /app
COPY package.json bun.lock ./
RUN --mount=type=cache,target=/root/.bun/install/cache \
    bun install --frozen-lockfile
COPY . .
RUN bun run build

FROM oven/bun:1-alpine
WORKDIR /app
ENV NODE_ENV=production
COPY --from=builder /app/dist ./dist
COPY --from=builder /app/node_modules ./node_modules
COPY package.json bun.lock ./
RUN addgroup -S app && adduser -S app -G app
USER app
EXPOSE 3000
CMD ["bun", "dist/index.js"]
