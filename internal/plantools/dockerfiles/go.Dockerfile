# keywords: golang
# description: Go multi-stage build: golang:alpine builder, distroless/static runtime
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

FROM gcr.io/distroless/static:nonroot
WORKDIR /app
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["/app/server"]
