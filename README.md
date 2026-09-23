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

Os testes são organizados entre testes unitários (puros, sem I/O ou dependências externas) e testes de integração (exercitando PostgreSQL real via Docker Compose).

### 1. Testes Unitários de Domínio (rápidos, sem containers)

Cobrem parsing monetário sem floats, regras de negócio dos 5 tipos de aposta, cálculo de hash canônico e invariantes aritméticas do ledger:

```sh
# Executar todos os testes unitários
go test -v ./internal/domain/...

# Executar testes unitários específicos
go test -v ./internal/domain/money/...
go test -v ./internal/domain/wallet/...
go test -v ./internal/domain/transaction/...
go test -v ./internal/domain/ledger/...
```

### 2. Análise Estática (Vet)

```sh
go vet ./...
```

### 3. Teste com Detector de Condições de Corrida (`-race`)

> **Nota**: O detector de condições de corrida (`-race`) exige CGO habilitado e um compilador C (`gcc`). Em ambientes Linux / macOS / CI ou Windows com MinGW configurado:

```sh
# No PowerShell (Windows com MinGW / 64-bit):
$env:GOARCH="amd64"
go test -race ./internal/domain/...

# No Linux / macOS / Bash:
GOARCH=amd64 go test -race ./internal/domain/...
```

### 4. Testes de Integração (com Containers Reais)

Exercitam a camada de persistência com `pgx/v5` e os casos de uso ponta a ponta (concorrência de 2 apostas de 80.00 sobre saldo de 100.00, idempotência, reversões antecipadas e reconciliação):

```sh
# 1. Certifique-se de que o PostgreSQL está rodando e migrado
docker compose up -d postgres
docker compose run --rm migrate

# 2. Executar todos os testes de integração
# No PowerShell:
$env:GOARCH="amd64"
go test -v -tags=integration ./...

# No Linux / Bash:
GOARCH=amd64 go test -v -tags=integration ./...

# Executar testes unitários de métricas e observabilidade:
go test -v ./internal/infra/observability/...

# Executar apenas testes de integração dos casos de uso:
go test -v -tags=integration ./internal/application/usecase/...

# Executar apenas testes de integração dos repositórios pgx:
go test -v -tags=integration ./internal/infra/db/postgres/...

# Executar testes de integração da API HTTP e roteamento:
go test -v -tags=integration ./internal/http/...

# Executar testes de integração do Consumidor SQS FIFO:
go test -v -tags=integration ./internal/worker/sqs/...

# Executar testes de integração do Outbox Worker:
go test -v -tags=integration ./internal/worker/outbox/...

# Executar teste de composição Uber Fx e ciclo de vida:
go test -v -tags=integration ./cmd/server/...
```

### 5. Cenário de Execução com Múltiplas Instâncias

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

### Health checks e Métricas
 
```sh
# Health checks
curl http://localhost:3000/health/live
curl http://localhost:3000/health/ready

# Métricas Prometheus
curl http://localhost:3000/metrics
```

## Estrutura do Projeto

```
desafio/
├── cmd/server/          # Entrypoint da aplicação e composição Fx
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
│   │   ├── port/        # Interfaces de repositórios e Unit of Work
│   │   └── usecase/     # Casos de uso e regras de negócio
│   ├── infra/
│   │   ├── auth/        # Validação JWT Keycloak (JWKS)
│   │   ├── config/      # Leitura e validação de env vars
│   │   ├── db/
│   │   │   ├── migrations/  # Migrations SQL versionadas
│   │   │   └── postgres/    # Pool pgx + repositórios PostgreSQL
│   │   └── observability/   # Métricas Prometheus e logger JSON
│   ├── http/
│   │   ├── handler/     # HTTP handlers
│   │   └── middleware/  # Auth, logging, correlation, recovery
│   └── worker/
│       ├── outbox/      # Worker de publicação da outbox (SKIP LOCKED)
│       ├── pendingref/  # Worker de resolução de referências pendentes
│       └── sqs/         # Consumidor SQS FIFO com deduplicação Inbox
├── scripts/
│   ├── keycloak/        # Realm export para auto-import
│   └── localstack/      # Script de provisionamento SQS FIFO
├── docker-compose.yml
├── Dockerfile
├── .env.example
├── ARCHITECTURE.md
└── README.md
```

Consulte `ARCHITECTURE.md` para decisões técnicas detalhadas.
