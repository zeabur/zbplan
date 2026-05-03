# keywords: cargo
# description: Rust multi-stage: rust-slim builder with cargo/registry cache, debian-slim runtime
FROM rust:1.95-slim AS builder
WORKDIR /app
COPY . .
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/app/target \
    cargo build --release && \
    find target/release -maxdepth 1 -type f -executable -not -name '*.d' \
         -exec cp {} /app/server \;

FROM debian:bookworm-slim
WORKDIR /app
COPY --from=builder /app/server .
RUN addgroup --system app && adduser --system --ingroup app app
USER app
EXPOSE 8080
CMD ["/app/server"]
