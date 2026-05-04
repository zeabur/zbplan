# keywords: nuxt nitro vue node server javascript typescript ssr
# description: Nuxt SSR/Node-server build (NITRO_PRESET=node-server), pnpm + node-alpine runtime
FROM node:24-alpine AS builder
WORKDIR /app
RUN corepack enable pnpm
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    --mount=type=bind,source=package.json,target=package.json \
    --mount=type=bind,source=pnpm-lock.yaml,target=pnpm-lock.yaml \
    pnpm install --frozen-lockfile
COPY . .
ENV NITRO_PRESET=node-server
RUN pnpm build

FROM node:24-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production
COPY --from=builder /app/.output ./.output
RUN addgroup -S app && adduser -S app -G app
USER app
EXPOSE 3000
CMD ["node", ".output/server/index.mjs"]
