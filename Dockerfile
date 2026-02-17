FROM rust:1.84-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app

# Copy manifests only to cache dependency layer
COPY Cargo.toml Cargo.lock ./
COPY crates/oasis-core/Cargo.toml crates/oasis-core/Cargo.toml
COPY crates/oasis-llm/Cargo.toml crates/oasis-llm/Cargo.toml
COPY crates/oasis-telegram/Cargo.toml crates/oasis-telegram/Cargo.toml
COPY crates/oasis-integrations/Cargo.toml crates/oasis-integrations/Cargo.toml
COPY crates/oasis-brain/Cargo.toml crates/oasis-brain/Cargo.toml

# Create dummy sources so cargo can resolve the workspace
RUN mkdir -p src && echo "fn main() {}" > src/main.rs \
    && for crate in oasis-core oasis-llm oasis-telegram oasis-integrations oasis-brain; do \
         mkdir -p crates/$crate/src && echo "" > crates/$crate/src/lib.rs; \
       done

# Build dependencies only (this layer gets cached)
RUN cargo build --release 2>/dev/null || true

# Copy real source and rebuild
COPY . .
RUN touch src/main.rs crates/*/src/lib.rs \
    && cargo build --release

FROM alpine:3.21
RUN apk add --no-cache chromium nss freetype harfbuzz ca-certificates ttf-freefont
COPY --from=builder /app/target/release/oasis /usr/local/bin/oasis
COPY oasis.toml /etc/oasis/oasis.toml
ENTRYPOINT ["oasis"]
