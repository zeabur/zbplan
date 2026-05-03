# keywords: cargo
# description: Rust multi-stage: rust-slim builder with cargo/registry cache, distroless/cc runtime
FROM rust:1.78-slim AS builder
WORKDIR /app
COPY Cargo.toml Cargo.lock ./
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/app/target \
    cargo fetch
COPY . .
RUN --mount=type=cache,target=/usr/local/cargo/registry \
    --mount=type=cache,target=/app/target \
    cargo build --release && \
    find target/release -maxdepth 1 -type f -executable -not -name '*.d' \
         -exec cp {} /app/server \;

FROM gcr.io/distroless/cc:nonroot
WORKDIR /app
COPY --from=builder /app/server .
EXPOSE 8080
CMD ["/app/server"]
