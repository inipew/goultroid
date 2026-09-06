# Stage 1: Build binary
# Keep this aligned with go.mod and CI: Go 1.27 is the supported toolchain.
FROM golang:1.27-alpine AS builder

WORKDIR /app

# Download dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build statically linked binary
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o bin/goultroid ./cmd/goultroid

# Stage 2: Minimal runtime
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /app/bin/goultroid .

RUN mkdir -p data && chmod 700 data

VOLUME ["/app/data"]

ENTRYPOINT ["./goultroid"]
