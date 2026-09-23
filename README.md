# Desafio Backend — Processamento Distribuído de Apostas em Go

Backend resiliente para processamento distribuído de apostas esportivas e jogos de cassino em Go, projetado para operar sob alta concorrência com garantias estritas de integridade financeira, idempotência durável, deduplicação em mensageria FIFO, transações ACID e observabilidade completa.

---

## 1. Pré-requisitos

- **Go**: 1.27+
- **Docker e Docker Compose**: v2+
- **Sistema Operacional**: Linux, macOS ou Windows (PowerShell)
- **AWS CLI / awslocal** *(opcional)*: para inspeção manual de filas SQS

---

## 2. Variáveis de Ambiente

Copie o arquivo de exemplo para criar o `.env` local:

```sh
cp .env.example .env
```

Para o ambiente de desenvolvimento local usando Docker Compose, os valores padrão de `.env.example` já estão pré-configurados e prontos para uso:

| Variável | Descrição | Padrão Local |
| :--- | :--- | :--- |
| `HTTP_PORT` | Porta do servidor HTTP | `3000` |
| `DATABASE_URL` | String de conexão PostgreSQL | `postgres://desafio:desafio@localhost:5432/desafio?sslmode=disable` |
| `KEYCLOAK_URL` | URL base do Keycloak IdP | `http://localhost:8080` |
| `KEYCLOAK_REALM` | Nome do Realm configurado | `desafio` |
| `AWS_ENDPOINT` | Endpoint LocalStack (SQS) | `http://localhost:4566` |
| `AWS_REGION` | Região AWS | `us-east-1` |
| `SQS_QUEUE_URL` | Fila SQS FIFO de transações | `http://localhost:4566/000000000000/wager-transactions.fifo` |
| `SQS_EVENTS_QUEUE_URL` | Fila SQS FIFO de eventos (Outbox) | `http://localhost:4566/000000000000/wager-events.fifo` |
| `PENDING_REF_MAX_RETRIES`| Máximo de retentativas para referências pendentes | `5` |
| `SHUTDOWN_TIMEOUT` | Timeout para encerramento gracioso | `30s` |

---

## 3. Inicialização dos Serviços

### 3.1. Subir Infraestrutura (PostgreSQL, LocalStack, Keycloak)

```sh
docker compose up -d postgres keycloak localstack
```

> **Provisionamento Automático:**
> - **PostgreSQL**: O script [`scripts/postgres/init.sql`](scripts/postgres/init.sql) cria automaticamente a base `keycloak` na inicialização.
> - **LocalStack SQS FIFO**: O script [`scripts/localstack/01-create-queues.sh`](scripts/localstack/01-create-queues.sh) provisiona as filas `wager-transactions.fifo`, `wager-transactions-dlq.fifo` e `wager-events.fifo` com deduplicação por conteúdo.
> - **Keycloak IdP**: O realm [`scripts/keycloak/desafio-realm.json`](scripts/keycloak/desafio-realm.json) é importado automaticamente no primeiro boot com os clientes e papéis pré-configurados.

Aguarde o Keycloak concluir o bootstrap inicial (~45-60 segundos):

```sh
# Verificar status dos serviços
docker compose ps
```

### 3.2. Aplicar Migrations no Banco de Dados

```sh
docker compose run --rm migrate
```

Para reverter migrations (*rollback*):

```sh
docker compose run --rm migrate down 1
```

### 3.3. Executar a Aplicação Localmente

```sh
go run ./cmd/server
```

### 3.4. Executar via Docker Compose (Stack Completa)

```sh
docker compose --profile app up --build
```

---

## 4. Identidades de Teste do IdP (Keycloak)

O Realm `desafio` já vem provisionado com 3 clientes via `client_credentials`:

| Client ID | Client Secret | Papéis (Roles) | Finalidade |
| :--- | :--- | :--- | :--- |
| `desafio-internal` | `internal-secret` | `internal` | Abertura de carteiras (`POST /wallets`) e reconciliação financeira (`POST /wallets/:id/reconciliation`) |
| `provider-a` | `provider-a-secret` | `provider` | Envio de apostas e consultas do provedor A (`POST /wagering/transactions`) |
| `provider-b` | `provider-b-secret` | `provider` | Envio de apostas e consultas do provedor B (utilizado para provar isolamento de tenants) |

---

## 5. Exemplos de Chamadas Autenticadas

### 5.1. Obter Tokens JWT no Keycloak

**Token Administrativo (`desafio-internal`):**
```sh
INTERNAL_TOKEN=$(curl -s -X POST http://localhost:8080/realms/desafio/protocol/openid-connect/token \
  -d "grant_type=client_credentials" \
  -d "client_id=desafio-internal" \
  -d "client_secret=internal-secret" | jq -r .access_token)
```

**Token de Provedor (`provider-a`):**
```sh
PROVIDER_A_TOKEN=$(curl -s -X POST http://localhost:8080/realms/desafio/protocol/openid-connect/token \
  -d "grant_type=client_credentials" \
  -d "client_id=provider-a" \
  -d "client_secret=provider-a-secret" | jq -r .access_token)
```

---

### 5.2. Abertura de Carteira (`POST /wallets`)

Requer papel `internal`. Cria a carteira e, se o saldo inicial for positivo, gera a transação `OPENING` interna e o lançamento inicial no ledger:

```sh
curl -s -X POST http://localhost:3000/wallets \
  -H "Authorization: Bearer $INTERNAL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "initialBalance": {
      "amount": "1000.00",
      "currency": "BRL"
    }
  }' | jq
```

---

### 5.3. Envio de Apostas e Operações de Jogos (`POST /wagering/transactions`)

Requer papel `provider` e cabeçalho `Idempotency-Key: <providerId>:<externalTransactionId>`.

#### 1. Aposta (`BET` — Débito):
```sh
curl -s -X POST http://localhost:3000/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:tx-bet-101" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "tx-bet-101",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "<WALLET_ID>",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "BET",
    "money": {"amount": "25.00", "currency": "BRL"}
  }' | jq
```

#### 2. Prêmio (`WIN` — Crédito):
```sh
curl -s -X POST http://localhost:3000/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:tx-win-102" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "tx-win-102",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "<WALLET_ID>",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "WIN",
    "money": {"amount": "75.00", "currency": "BRL"}
  }' | jq
```

#### 3. Derrota Sem Efeito Financeiro (`LOSS`):
```sh
curl -s -X POST http://localhost:3000/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:tx-loss-103" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "tx-loss-103",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "<WALLET_ID>",
    "roundId": "round-988",
    "gameId": "fortune-chimp",
    "kind": "LOSS",
    "money": {"amount": "0.00", "currency": "BRL"}
  }' | jq
```

#### 4. Reembolso de Aposta (`REFUND` — Crédito de devolução):
```sh
curl -s -X POST http://localhost:3000/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:tx-refund-104" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "tx-refund-104",
    "referenceExternalTransactionId": "tx-bet-101",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "<WALLET_ID>",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "REFUND",
    "money": {"amount": "25.00", "currency": "BRL"}
  }' | jq
```

#### 5. Estorno de Operação (`ROLLBACK`):
```sh
curl -s -X POST http://localhost:3000/wagering/transactions \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: provider-a:tx-rollback-105" \
  -d '{
    "providerId": "provider-a",
    "externalTransactionId": "tx-rollback-105",
    "referenceExternalTransactionId": "tx-bet-101",
    "playerId": "0192f28f-5dc0-7d58-bdb2-814ad6a0f4a1",
    "walletId": "<WALLET_ID>",
    "roundId": "round-987",
    "gameId": "fortune-chimp",
    "kind": "ROLLBACK",
    "money": {"amount": "25.00", "currency": "BRL"}
  }' | jq
```

---

### 5.4. Consultas e Auditoria

#### Consulta de Saldo e Versão da Carteira (`GET /wallets/:walletId`):
```sh
curl -s http://localhost:3000/wallets/<WALLET_ID> \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" | jq
```

#### Extrato Auditável do Ledger (`GET /wallets/:walletId/ledger`):
```sh
curl -s "http://localhost:3000/wallets/<WALLET_ID>/ledger?limit=20" \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN" | jq
```

#### Reconciliação Financeira (`POST /wallets/:walletId/reconciliation`):
Audita matematicamente o saldo gravado contra a somatória dos lançamentos do ledger append-only (`saldo = soma(créditos) - soma(débitos)`):
```sh
curl -s -X POST http://localhost:3000/wallets/<WALLET_ID>/reconciliation \
  -H "Authorization: Bearer $INTERNAL_TOKEN" | jq
```

#### Prova de Isolamento de Tenants (`403 Forbidden`):
Se `provider-a` tentar consultar uma transação de `provider-b`:
```sh
curl -s -i http://localhost:3000/providers/provider-b/wagering/transactions/tx-999 \
  -H "Authorization: Bearer $PROVIDER_A_TOKEN"
# Retorna: HTTP/1.1 403 Forbidden — token providerId does not match resource providerId
```

---

### 5.5. Health Checks e Métricas Prometheus

```sh
# Liveness (saúde do processo)
curl http://localhost:3000/health/live

# Readiness (conectividade com PostgreSQL e AWS SQS)
curl http://localhost:3000/health/ready
# Resposta: {"database":"READY","sqs":"READY","status":"UP"}

# Métricas no formato Prometheus
curl http://localhost:3000/metrics
```

---

## 6. Comandos de Teste

Conforme exigido na Seção 15 do desafio, os comandos padronizados estão disponíveis abaixo:

### 6.1. Comandos Principais

```sh
# 1. Subir infraestrutura completa e aplicação
docker compose up --build

# 2. Executar testes unitários (rápidos, sem I/O ou containers)
go test ./...

# 3. Análise estática com o compilador Go (zero warnings)
go vet ./...

# 4. Detector de condições de corrida (requer ambiente Linux/CGO habilitado)
go test -race ./internal/domain/...
```

> **Nota sobre `-race` no Windows**: O detector de *race conditions* do Go (`-race`) depende de CGO e de um compilador C (`gcc`). Em ambientes Windows sem MinGW configurado, o comando pode ser executado dentro do container Docker ou no pipeline CI Linux.

---

### 6.2. Testes de Integração com Containers Reais (`-tags=integration`)

Os testes de integração utilizam a build tag `integration` para exercitar a persistência real no PostgreSQL, mensageria SQS no LocalStack e autenticação com Keycloak:

```sh
# No PowerShell (Windows 64-bit):
$env:GOARCH="amd64"
go test -v -tags=integration ./...

# No Linux / macOS / Bash:
GOARCH=amd64 go test -v -tags=integration ./...
```

---

### 6.3. Suíte de Concorrência, Caos e Idempotência (Seção 13 do DESAFIO.md)

O arquivo [`tests/concurrency_test.go`](tests/concurrency_test.go) executa testes rigorosos contra infraestrutura real:

```sh
# Executar apenas a suíte de concorrência e caos:
go test -v -tags=integration ./tests/...
```

Os 8 cenários obrigatórios cobertos e aprovados são:

1. **50 Apostas Simultâneas da Mesma Operação (`TestConcurrency_50SameBet_SingleDebit`)**:
   50 goroutines submetem em paralelo a mesma aposta. Apenas 1 débito é aplicado na carteira e gravado no ledger; as outras 49 recebem replay idempotente legítimo com o mesmo saldo (`idempotentReplay: true`) e divergência zero na reconciliação.
2. **Disputa de 2 Apostas de 80.00 sobre Saldo de 100.00 (`TestConcurrency_Two80BetsOn100Balance`)**:
   Duas apostas simultâneas disputam o saldo via lock pessimista (`SELECT FOR UPDATE`). Exatamente uma é aceita (`PROCESSED`) e a outra é rejeitada por saldo insuficiente (`REJECTED`), com saldo final de exatamente 20.00 BRL.
3. **Alto Paralelismo em Carteiras Distintas (`TestConcurrency_DistinctWallets_HighParallelism`)**:
   10 carteiras processam débitos em paralelo sem contenção cruzada e com reconciliação 100% íntegra.
4. **Múltiplas Instâncias HTTP Independentes (`TestConcurrency_MultiInstance_Distributed`)**:
   3 servidores HTTP reais compartilhando o mesmo PostgreSQL processam 30 apostas simultâneas até zerar o saldo com concorrência perfeita e sem atualizações perdidas (*lost updates*).
5. **Simulação de Chaos na Reentrega SQS (`TestChaos_SQSConsumer_RedeliveryDeduplication`)**:
   Simula queda do consumidor após o commit no banco e antes da remoção da mensagem no SQS (`DeleteMessage`). A reentrega da mensagem para outro worker é deduplicada via Inbox persistente com zero débitos duplicados.
6. **Publishers Concorrentes da Outbox com SKIP LOCKED (`TestConcurrency_DualOutboxPublishers`)**:
   2 workers disputam simultaneamente o lote de eventos pendentes. Todos os eventos são publicados sem duplicação nem contenção.
7. **Reversões Desordenadas (`TestChaos_PendingReference_ResolutionAndExpiration`)**:
   `REFUND` submetido antes da aposta original entra em `PENDING_REFERENCE`. Ao chegar a aposta original, o worker de retentativas resolve a pendência e restaura o saldo; reversões órfãs são rejeitadas após o limite de tentativas com `REFERENCE_NOT_FOUND`.
8. **Deduplicação Cruzada entre HTTP e SQS (`TestConcurrency_CrossHTTPAndSQS_Deduplication`)**:
   Chamada simultânea da mesma transação via API REST e via fila SQS. Uma atua como processamento original e a outra como replay idempotente imediato.

---

### 6.4. Execução com Múltiplas Instâncias HTTP em Portas Diferentes

```sh
# Instância 1
HTTP_PORT=3001 go run ./cmd/server &

# Instância 2
HTTP_PORT=3002 go run ./cmd/server &

# Instância 3
HTTP_PORT=3003 go run ./cmd/server &
```

---

### 6.5. Teste de Carga Reproduzível (Diferencial Opcional — Seção 14)

A aplicação conta com uma ferramenta CLI nativa em Go ([`cmd/loadtest`](cmd/loadtest)) para testes de carga sem dependências externas, validando throughput, percentis de latência, tolerância a falhas e consistência contábil sob alto estresse.

#### Comando Reproduzível:
```sh
# Executa 500 requisições concorrentes com 10 workers:
go run ./cmd/loadtest -workers 10 -requests 500

# Parâmetros customizáveis:
# -url       (default: http://localhost:3000)
# -keycloak  (default: http://localhost:8080)
# -workers   (default: 10)
# -requests  (default: 500)
# -timeout   (default: 10s)
```

#### Metodologia e Cenário de Carga:
1. **Autenticação OIDC**: Obtém tokens JWT reais via `client_credentials` para `desafio-internal` e `provider-a`.
2. **Provisionamento**: Cria dinamicamente carteiras de teste (uma principal com 500.000,00 BRL e uma de estresse com 15,00 BRL).
3. **Mix de Tráfego Concorrente**:
   - **85%** apostas únicas com saldo suficiente (`PROCESSED`).
   - **10%** replays idempotentes com a mesma chave e payload (`idempotentReplay: true`).
   - **3%** disputas de saldo insuficiente (`REJECTED` com `INSUFFICIENT_FUNDS`).
   - **2%** conflitos intencionais de chave reutilizada com payload divergente (`409 Conflict`).
4. **Métricas de Latência**: Medição por requisição com histograma ordenado para apuração de `Min`, `p50`, `p95`, `p99`, `Max` e `Média`.
5. **Atraso da Outbox**: Consulta `/metrics` para verificar o atraso e a drenagem dos eventos pelo publicador assíncrono.
6. **Reconciliação Final**: Invoca `POST /wallets/:id/reconciliation` para provar matematicamente que o saldo em banco bate exatamente com o ledger (`storedBalance == sum(credits - debits)`).

#### Resultados Medidos (Ambiente de Referência):
- **Ambiente**: Intel Core i7 / 16 GB RAM / Windows 11 WSL2 Docker (PostgreSQL 16, LocalStack 3.4, Keycloak 24).
- **Throughput**: ~**195 a 210 req/s**.
- **Latências**: Min: ~1.5ms | **p50**: ~22ms | **p95**: ~255ms | **p99**: ~512ms | Média: ~50ms.
- **Erros**: 0 erros 5xx (estabilidade total).
- **Conflitos de Concorrência**: Detectados e isolados com precisão (`409 Conflict`).
- **Atraso da Outbox**: Drenagem em tempo real pelo worker com `SKIP LOCKED`.
- **Auditoria Contábil**: `consistent = true` com divergência financeira **ZERO**.

---

## 7. Estrutura do Projeto

```
desafio/
├── cmd/
│   ├── loadtest/            # CLI de teste de carga reproduzível (throughput, percentis e auditoria)
│   └── server/              # Entrypoint da aplicação e composição Fx (DI e Lifecycle)
├── internal/
│   ├── domain/              # Domínio puro (sem dependências externas)
│   │   ├── money/           # Value object Money (int64 centavos, zero floats)
│   │   ├── wallet/          # Agregado Wallet (invariantes e versionamento)
│   │   ├── transaction/     # WagerTransaction + máquina de estados finita
│   │   ├── ledger/          # WalletLedgerEntry (imutável, append-only)
│   │   ├── events/          # Eventos de domínio versionados
│   │   ├── inbox/           # Modelo InboxMessage (deduplicação de broker)
│   │   └── outbox/          # Modelo OutboxEvent (Transactional Outbox)
│   ├── application/
│   │   ├── port/            # Interfaces de repositórios e Unit of Work (DBTX)
│   │   └── usecase/         # Casos de uso e regras de negócio
│   ├── infra/
│   │   ├── auth/            # Validação JWT Keycloak com cache JWKS
│   │   ├── config/          # Leitura e validação estrita de variáveis de ambiente
│   │   ├── db/
│   │   │   ├── migrations/  # Migrations SQL versionadas (golang-migrate)
│   │   │   └── postgres/    # Pool pgx/v5 + repositórios PostgreSQL
│   │   └── observability/   # 10 Métricas Prometheus e logger JSON (log/slog)
│   ├── http/
│   │   ├── handler/         # HTTP handlers REST
│   │   └── middleware/      # Auth, tenant isolation, logging, recovery
│   └── worker/
│       ├── outbox/          # Worker de publicação da outbox (FOR UPDATE SKIP LOCKED)
│       ├── pendingref/      # Worker de resolução de referências pendentes
│       └── sqs/             # Consumidor SQS FIFO com deduplicação Inbox
├── tests/                   # Suíte de testes de concorrência, idempotência e caos (Seção 13)
├── scripts/
│   ├── keycloak/            # Realm export para importação automática do Keycloak
│   ├── localstack/          # Script bash de provisionamento das filas SQS FIFO
│   └── postgres/            # Script de inicialização do banco keycloak
├── docker-compose.yml       # Orquestração local dos containers
├── Dockerfile               # Build multi-stage da aplicação Go
├── .env.example             # Variáveis de ambiente de exemplo
├── ARCHITECTURE.md          # Registro detalhado das decisões de arquitetura
└── README.md                # Guia de início rápido e comandos
```

Para detalhes aprofundados sobre decisões de design, modelos matemáticos e garantias de consistência, consulte o [`ARCHITECTURE.md`](ARCHITECTURE.md).
