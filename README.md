# Desafio Backend — Processamento Distribuído de Apostas em Go

## Pré-requisitos

- Go 1.27+
- Docker e Docker Compose v2+
- `awslocal` (opcional, para inspeção manual das filas)

## Variáveis de Ambiente

Copie o arquivo de exemplo e ajuste se necessário:

```sh
cp .env.example .env
```

As variáveis estão documentadas no `.env.example`. Para o ambiente local com Docker Compose, os valores padrão já funcionam sem alteração.

## Inicialização

### 1. Subir infraestrutura

```sh
docker compose up -d postgres keycloak localstack
```

> Aguarde o Keycloak ficar disponível (~60s na primeira execução) antes de continuar.

### 2. Aplicar migrations

```sh
docker compose run --rm migrate
```

Para reverter:

```sh
docker compose run --rm migrate down 1
```

### 3. Executar a aplicação (desenvolvimento)

```sh
cp .env.example .env
go run ./cmd/server
```

### 4. Executar via Docker Compose (produção local)

```sh
docker compose --profile app up --build
```

## Testes

```sh
# Todos os testes
go test ./...

# Com detector de race conditions
go test -race ./...

# Vet
go vet ./...
```

### Testes de integração

Requerem PostgreSQL, LocalStack e Keycloak rodando:

```sh
docker compose up -d postgres keycloak localstack
docker compose run --rm migrate
go test -tags=integration ./...
```

### Múltiplas instâncias

```sh
HTTP_PORT=3001 go run ./cmd/server &
HTTP_PORT=3002 go run ./cmd/server &
HTTP_PORT=3003 go run ./cmd/server &
```

## Exemplos de Chamadas

### Obter token (provider-a)

```sh
curl -s -X POST http://localhost:8080/realms/desafio/protocol/openid-connect/token \
  -d "grant_type=client_credentials" \
  -d "client_id=provider-a" \
  -d "client_secret=provider-a-secret" | jq .access_token
```

### Abrir carteira

```sh
curl -s -X POST http://localhost:3000/wallets \
  -H "Authorization: Bearer <TOKEN>" \
  -H "Content-Type: application/json" \
  -d '{"playerId":"0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1","initialBalance":{"amount":"1000.00","currency":"BRL"}}' | jq
```

### Enviar aposta

```sh
curl -s -X POST http://localhost:3000/wagering/transactions \
  -H "Authorization: Bearer <TOKEN>" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:transaction-123" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "transaction-123",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "<WALLET_ID>",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }' | jq
```

### Health checks

```sh
curl http://localhost:3000/health/live
curl http://localhost:3000/health/ready
```

## Estrutura do Projeto

```
desafio/
├── cmd/server/          # Entrypoint da aplicação
├── internal/
│   ├── domain/          # Domínio puro (sem dependências externas)
│   │   ├── money/       # Value object Money (int64 centavos)
│   │   ├── wallet/      # Agregado Wallet
│   │   ├── transaction/ # WagerTransaction + máquina de estados
│   │   ├── ledger/      # WalletLedgerEntry (imutável)
│   │   ├── events/      # Eventos de domínio
│   │   ├── inbox/       # Modelo InboxMessage
│   │   └── outbox/      # Modelo OutboxEvent
│   ├── application/
│   │   └── usecase/     # Casos de uso (orquestração)
│   ├── infra/
│   │   ├── config/      # Configuração via env vars
│   │   ├── db/
│   │   │   ├── migrations/  # Migrations SQL versionadas
│   │   │   └── postgres/    # Pool pgx + runner de migrations
│   │   ├── sqs/         # Cliente SQS (LocalStack)
│   │   ├── auth/        # Validação JWT Keycloak
│   │   └── outbox/      # Worker de publicação outbox
│   ├── http/
│   │   ├── handler/     # HTTP handlers
│   │   └── middleware/  # Auth, logging, recovery
│   └── fx/              # Módulos Uber Fx
├── scripts/
│   ├── keycloak/        # Realm export para auto-import
│   └── localstack/      # Script de provisionamento SQS
├── docker-compose.yml
├── Dockerfile
├── .env.example
├── ARCHITECTURE.md
└── README.md
```

Consulte `ARCHITECTURE.md` para decisões técnicas detalhadas.
