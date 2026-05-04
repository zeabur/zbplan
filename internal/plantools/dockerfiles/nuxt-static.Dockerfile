# keywords: nuxt nitro vue static generate spa prerender
# description: Nuxt static-site build (nuxt generate), pnpm builder, nginx-alpine runtime serving .output/public
FROM node:24-alpine AS builder
WORKDIR /app
RUN corepack enable pnpm
RUN --mount=type=cache,target=/root/.local/share/pnpm/store \
    --mount=type=bind,source=package.json,target=package.json \
    --mount=type=bind,source=pnpm-lock.yaml,target=pnpm-lock.yaml \
    pnpm install --frozen-lockfile
COPY . .
RUN pnpm build

FROM nginx:alpine
COPY --from=builder /app/.output/public /usr/share/nginx/html
EXPOSE 80
CMD ["nginx", "-g", "daemon off;"]
