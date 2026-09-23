# ============================================================
# Build stage
# ============================================================
FROM golang:1.27-alpine AS builder

WORKDIR /app

# Instala dependências de sistema mínimas
RUN apk add --no-cache git ca-certificates

# Copia o módulo primeiro para aproveitar cache de layers
COPY go.mod go.sum ./
RUN go mod download

# Copia o restante do código
COPY . .

# Compila com flags de produção
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 \
    go build -ldflags="-s -w -extldflags=-static" \
    -o /app/bin/server ./cmd/server

# ============================================================
# Runtime stage
# ============================================================
FROM gcr.io/distroless/static-debian12

WORKDIR /app

COPY --from=builder /app/bin/server .
COPY --from=builder /app/internal/infra/db/migrations ./migrations

EXPOSE 3000

ENTRYPOINT ["/app/server"]
