# ---------------------------------------------------------------------------
# Stage 1: build the Angular frontend
# ---------------------------------------------------------------------------
FROM node:22-alpine AS web

WORKDIR /web
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY frontend/ ./
RUN npx ng build --configuration production

# ---------------------------------------------------------------------------
# Stage 2: build the Go service
# ---------------------------------------------------------------------------
FROM golang:1.24-alpine AS builder

WORKDIR /app

# Download dependencies separately to exploit Docker layer caching.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY migrations/ ./migrations/

RUN CGO_ENABLED=0 GOOS=linux \
    go build -trimpath -ldflags="-s -w" -o /kymo ./cmd/kymo

# ---------------------------------------------------------------------------
# Stage 3: runtime
# ---------------------------------------------------------------------------
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

# Run as a non-root user for security.
RUN addgroup -S kymo && adduser -S kymo -G kymo
USER kymo

COPY --from=builder /kymo /kymo
COPY --from=web /web/dist/frontend/browser /app/web

ENV KYMO_SERVER_STATIC_DIR=/app/web

EXPOSE 8080

ENTRYPOINT ["/kymo"]
