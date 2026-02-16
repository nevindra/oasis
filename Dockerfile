FROM rust:1.84-alpine AS builder
RUN apk add --no-cache musl-dev
WORKDIR /app
COPY . .
RUN cargo build --release

FROM alpine:3.21
RUN apk add --no-cache chromium nss freetype harfbuzz ca-certificates ttf-freefont
COPY --from=builder /app/target/release/oasis /usr/local/bin/oasis
COPY oasis.toml /etc/oasis/oasis.toml
ENTRYPOINT ["oasis"]
