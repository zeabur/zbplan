# keywords: caddy nginx vite spa react vue html static
# description: Static site: node-alpine builder (npm build), zeabur/caddy-static runtime with native SPA/MPA fallback and _headers/_redirects support
FROM node:24-alpine AS builder
WORKDIR /app
RUN --mount=type=cache,target=/root/.npm \
    --mount=type=bind,source=package.json,target=package.json \
    --mount=type=bind,source=package-lock.json,target=package-lock.json \
    npm ci
COPY . .
RUN npm run build

FROM zeabur/caddy-static:2
COPY --from=builder /app/dist /usr/share/caddy
