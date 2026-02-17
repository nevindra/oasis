FROM golang:1.24-alpine AS builder
RUN apk add --no-cache gcc musl-dev
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 go build -o oasis ./cmd/bot_example/

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /app/oasis /usr/local/bin/oasis
COPY oasis.toml /etc/oasis/oasis.toml
ENTRYPOINT ["oasis"]
