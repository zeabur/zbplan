# keywords: golang
# description: Go multi-stage build: golang:alpine builder, alpine runtime
FROM golang:1.24-alpine AS builder
WORKDIR /app
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=bind,source=go.mod,target=go.mod \
    --mount=type=bind,source=go.sum,target=go.sum \
    go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go build -o /app/server ./...

FROM alpine:3
WORKDIR /app
COPY --from=builder /app/server .
RUN addgroup -S app && adduser -S app -G app
USER app
EXPOSE 8080
CMD ["/app/server"]
