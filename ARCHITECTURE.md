# ARCHITECTURE.md — Desafio Backend: Processamento Distribuído de Apostas

> **Estado:** Atualizado até a Fase 1 (infraestrutura). Cada fase subsequente acrescenta seções.

---

## Índice

1. [Visão Geral](#1-visão-geral)
2. [Stack e Justificativas](#2-stack-e-justificativas)
3. [Representação de Dinheiro (Money)](#3-representação-de-dinheiro-money)
4. [Modelo de Banco de Dados e Schema](#4-modelo-de-banco-de-dados-e-schema)
5. [Transações SQL e Delimitação entre Repositórios](#5-transações-sql-e-delimitação-entre-repositórios)
6. [Controle de Concorrência](#6-controle-de-concorrência)
7. [Idempotência](#7-idempotência)
8. [Referências Pendentes](#8-referências-pendentes)
9. [Reversões (REFUND e ROLLBACK)](#9-reversões-refund-e-rollback)
10. [Inbox e Outbox](#10-inbox-e-outbox)
11. [Autenticação e Autorização](#11-autenticação-e-autorização)
12. [Composição com Uber Fx](#12-composição-com-uber-fx)
13. [Shutdown Gracioso](#13-shutdown-gracioso)
14. [Observabilidade](#14-observabilidade)
15. [Limitações, Interpretações e Trabalho Pendente](#15-limitações-interpretações-e-trabalho-pendente)

---

## 1. Visão Geral

O serviço é um processador de operações financeiras de provedores de jogos. Ele expõe uma **API HTTP** e consome mensagens de uma **fila SQS FIFO**, movimentando carteiras de jogadores com garantias financeiras equivalentes em ambas as entradas.

```
┌─────────────┐     HTTP      ┌──────────────────────────────────────────┐
│  Provider   │──────────────▶│                                          │
│  (Keycloak) │               │            Application Service           │
└─────────────┘               │                                          │
                               │  ┌────────────┐   ┌──────────────────┐ │
┌─────────────┐     SQS FIFO  │  │  Use Cases │   │   Domain Model   │ │
│  SQS Queue  │──────────────▶│  │            │──▶│  Wallet / Txn /  │ │
│  (LocalSt.) │               │  │  Wallet    │   │  Money / Ledger  │ │
└─────────────┘               │  │  Wager     │   └──────────────────┘ │
                               │  │  Reconcile │           │             │
                               │  └────────────┘    ┌──────▼──────┐    │
                               │                     │ PostgreSQL  │    │
                               │  ┌────────────┐    │ (pgx pool)  │    │
                               │  │  Workers   │    └─────────────┘    │
                               │  │  Outbox    │                        │
                               │  │  PendRef   │──▶ SQS Events Queue   │
                               │  └────────────┘                        │
                               └──────────────────────────────────────────┘
```

**Garantias centrais implementadas:**

| Garantia | Mecanismo |
|---|---|
| Sem float em dinheiro | `BIGINT` no banco; `int64` no domínio |
| Idempotência persistente | Unicidade de `idempotency_key` + `(provider_id, external_transaction_id)` no banco |
| Sem saldo negativo por concorrência | `SELECT FOR UPDATE` por carteira + `CHECK (balance_amount >= 0)` |
| Ledger imutável | `RULE` de banco + constraint de unicidade `(wallet_id, transaction_id)` |
| Publicação após commit | Transactional Outbox Pattern |
| Recuperação entre instâncias | Estado persistido; workers stateless |

---

## 2. Stack e Justificativas

### Linguagem e Runtime

- **Go 1.27** — versão atual; suporte nativo a `log/slog` (JSON handler), generics maduros e `context`-driven concurrency.

### Biblioteca de acesso ao banco

**`pgx/v5`** com SQL explícito.

Justificativa:
- SQL explícito torna transações, locks e constraints visíveis e auditáveis — requisito direto do desafio.
- `pgx` tem performance superior a `database/sql` + `lib/pq` para PostgreSQL, com suporte nativo a tipos como `pgtype.Numeric` e batch queries.
- O ORM (GORM) foi descartado por mascarar as fronteiras de transação e dificultar o uso de `SELECT FOR UPDATE` e `SKIP LOCKED`.
- `sqlc` foi considerado mas omitido para manter a solução sem geração de código adicional no prazo de 3 dias.

### HTTP Router

**`chi`** — roteador leve compatível com `net/http`, sem frameworks opinionados. Permite middlewares padrão e composição simples.

### Composição

**`go.uber.org/fx`** — injeção de dependência declarativa por construtores, com `fx.Module`, `fx.Provide`, `fx.Invoke` e `fx.Lifecycle`. O domínio não importa Fx em nenhuma camada.

### Mensageria

**AWS SQS FIFO** via `aws-sdk-go-v2`, executado localmente com **LocalStack 3.4**. FIFO garante ordenação por `MessageGroupId` (por `walletId`) e deduplicação de infra adicional via `MessageDeduplicationId`.

### Autenticação

**Keycloak 24** com fluxo `client_credentials` (OAuth 2.0). Validação de JWT sem estado no serviço — apenas JWKS fetch + verificação de assinatura e claims.

### Logs

**`log/slog`** (stdlib Go 1.21+) com `JSONHandler`. Sem dependência externa. Campos obrigatórios de rastreamento: `correlationId`, `transactionId`, `walletId`, `providerId`, `messageId`.

### Métricas

**`prometheus/client_golang`** exposto em `/metrics`. Contadores e histogramas para resultados de transação, duplicatas, retries, DLQ, latência e atraso da outbox.

### Migrations

**`golang-migrate/migrate/v4`** com source `file://` e driver `postgres`. Migrations versionadas com arquivo `.up.sql` e `.down.sql` separados. Aplicadas automaticamente no startup ou via `docker compose run --rm migrate`.

---

## 3. Representação de Dinheiro (Money)

### Representação interna

`Money` é um **value object imutável** com dois campos:

```go
type Money struct {
    amount   int64      // centavos (unidades mínimas)
    currency Currency   // código ISO 4217: "BRL", "USD", "EUR"
}
```

**Escala fixa de 2 casas decimais** para todas as moedas suportadas. Exemplos:

| Valor externo (string) | Representação interna (int64) |
|---|---|
| `"1000.00"` | `100000` |
| `"25.50"` | `2550` |
| `"0.01"` | `1` |
| `"0.00"` | `0` |

**Limites do `int64`:** `math.MaxInt64` = 9.223.372.036.854.775.807 centavos ≈ 92 trilhões de reais. Adequado para o escopo do desafio. Overflow é detectado explicitamente nas operações de soma e subtração.

### Serialização externa

O contrato externo usa strings decimais para evitar perda de precisão em parsers JSON:

```json
{ "amount": "25.00", "currency": "BRL" }
```

A conversão `string → int64` rejeita:
- Valores vazios ou não numéricos
- `NaN`, `Infinity`, notação científica (`1e5`)
- Escala excedente (mais de 2 casas decimais)
- Valores negativos em entradas financeiras externas

### Persistência no banco

```sql
money_amount   BIGINT        NOT NULL  -- centavos
money_currency currency_code NOT NULL  -- enum ISO 4217
```

Sem `NUMERIC`, sem `DECIMAL`, sem `FLOAT`. O mapeamento `int64 ↔ BIGINT` é direto e sem perda.

### Hash de idempotência e normalização

O hash é calculado **após** a conversão para `int64` e antes de qualquer operação de negócio. Isso garante que `"25.00"` e `"25.0"` produzam hashes distintos (entradas inválidas são rejeitadas antes do hash). Formas equivalentes **não** são silenciosamente normalizadas — entradas com escala inválida são rejeitadas com erro.

---

## 4. Modelo de Banco de Dados e Schema

### Tabelas e responsabilidades

| Tabela | Responsabilidade |
|---|---|
| `wallets` | Identidade, jogador, moeda, saldo, versão |
| `wager_transactions` | Todas as operações financeiras (internas e externas) |
| `wallet_ledger_entries` | Registro imutável de cada movimentação |
| `inbox_messages` | Deduplicação de mensagens SQS |
| `outbox_events` | Eventos de integração pendentes de publicação |
| `schema_migrations` | Controle de versão do `golang-migrate` |

### Invariantes garantidas pelo schema

```sql
-- Saldo nunca negativo
CHECK (balance_amount >= 0)

-- Versão da carteira sempre ≥ 1
CHECK (version >= 1)

-- Valor da movimentação sempre positivo no ledger
CHECK (money_amount > 0)

-- Saldo antes e depois sempre não-negativos
CHECK (balance_before >= 0)
CHECK (balance_after >= 0)

-- Coerência aritmética do ledger (imutável por constraint)
CHECK (direction != 'DEBIT'  OR balance_after = balance_before - money_amount)
CHECK (direction != 'CREDIT' OR balance_after = balance_before + money_amount)

-- OPENING não tem campos externos
CHECK (kind != 'OPENING' OR (provider_id IS NULL AND external_transaction_id IS NULL AND ...))

-- Não-OPENING tem provider e external_id
CHECK (kind = 'OPENING' OR (provider_id IS NOT NULL AND external_transaction_id IS NOT NULL))
```

### Imutabilidade do Ledger

O ledger é protegido por **duas camadas**:

1. **`RULE` de banco** — `ON UPDATE ... DO INSTEAD NOTHING` e `ON DELETE ... DO INSTEAD NOTHING` tornam UPDATE e DELETE silenciosamente ignorados no nível do PostgreSQL.
2. **Unicidade** — `UNIQUE (wallet_id, transaction_id)` impede segundo lançamento para a mesma transação.
3. **Sem soft-delete** — nenhuma coluna `deleted_at` existe no ledger.

### Enums PostgreSQL

Tipos `ENUM` para `transaction_kind`, `transaction_status`, `ledger_direction` e `currency_code` garantem que valores inválidos sejam rejeitados pelo banco antes de chegar ao domínio.

---

## 5. Transações SQL e Delimitação entre Repositórios

### Princípio fundamental

**Uma operação financeira = uma transação SQL.**

O caso de uso (`ProcessWagerTransactionUseCase`) abre a transação, orquestra os repositórios dentro dela e faz commit ou rollback ao final. Nenhum repositório abre ou fecha transações por conta própria.

### O que é confirmado atomicamente em uma única transação

```
BEGIN
  ├── INSERT inbox_messages          (deduplicação SQS, se origem for SQS)
  ├── SELECT wallets FOR UPDATE      (lock pessimista)
  ├── UPDATE wallets SET balance, version, updated_at
  ├── INSERT wager_transactions      (estado PROCESSED/REJECTED)
  ├── INSERT wallet_ledger_entries   (se houver movimentação)
  └── INSERT outbox_events           (eventos de integração)
COMMIT
```

Se qualquer passo falhar, toda a operação é revertida. Não há estado parcialmente gravado.

### Passagem de `*pgx.Tx` entre repositórios

Os repositórios recebem uma interface `db.Execer` que aceita tanto `*pgxpool.Pool` quanto `*pgx.Tx`. O caso de uso extrai a transação do pool e a passa explicitamente para cada repositório. Isso mantém os repositórios testáveis de forma isolada.

---

## 6. Controle de Concorrência

### Estratégia: Locking Pessimista por Carteira

**`SELECT ... FOR UPDATE`** na tabela `wallets` usando o `walletId` como chave de lock.

```sql
SELECT id, balance_amount, version, ...
FROM wallets
WHERE id = $1
FOR UPDATE
```

Justificativa da escolha:

| Estratégia | Prós | Contras | Decisão |
|---|---|---|---|
| **Pessimista (`FOR UPDATE`)** | Simples, sem retry storms, garantia absoluta | Serializa por carteira | ✅ **Escolhida** |
| Otimista (versão + retry) | Menor contention em baixa carga | Retry ilimitado pode violar SLAs; complexidade de rollback | ❌ |
| Atômica condicionada (`UPDATE ... WHERE version = $v`) | Sem lock explícito | Dificulta leitura consistente do saldo no mesmo statement | ❌ |

**Carteiras independentes avançam em paralelo** — o lock é granular por `walletId`, não global. Dois processos com carteiras distintas não se bloqueiam.

**Teste obrigatório (2x BET de 80.00 sobre saldo de 100.00):** A serialização por `FOR UPDATE` garante que apenas uma aposta é processada por vez para a mesma carteira. A segunda vê o saldo já debitado (20.00) e é rejeitada com `INSUFFICIENT_FUNDS`.

---

## 7. Idempotência

### Camadas de proteção

A idempotência é **persistente** (sobrevive a reinicializações) e opera em múltiplas camadas:

**Camada 1 — Unicidade de identificação externa (banco):**
```sql
UNIQUE (provider_id, external_transaction_id)
UNIQUE (idempotency_key)
```
Qualquer tentativa de inserir a mesma operação novamente falha com violação de unicidade, **mesmo em instâncias diferentes**.

**Camada 2 — Detecção de conflito de payload:**
O hash do payload é calculado em JSON canônico (chaves ordenadas lexicograficamente) sobre os campos de negócio, excluindo `Idempotency-Key` e metadados de transporte:

```
campos incluídos no hash:
  currency, externalTransactionId, gameId, kind,
  money.amount, money.currency, playerId, providerId,
  referenceExternalTransactionId (se presente), roundId, walletId
```

**Algoritmo:** `SHA-256` do JSON canônico em hex. Equivalente entre HTTP e SQS.

**Camada 3 — Inbox (SQS):**
Para entrada por SQS, `(consumer_name, message_id)` é registrado na `inbox_messages` dentro da mesma transação. Reentregas do mesmo `messageId` são detectadas e não reprocessadas.

### Comportamento por cenário

| Cenário | Comportamento |
|---|---|
| Chave + payload idênticos | Retorna resultado persistido, `idempotentReplay: true` |
| Chave reutilizada, payload diferente | HTTP 409 Conflict |
| Mesma operação com chave diferente | HTTP 409 Conflict (unicidade de `(provider_id, external_transaction_id)`) |
| Operação já terminal (replay) | Devolve saldo observado no processamento original |

### Saldo no replay

O campo `result_balance_amount` / `result_balance_currency` na tabela `wager_transactions` armazena o saldo da carteira **no momento do processamento original**. Replays retornam esse valor, mesmo que a carteira tenha sofrido outras movimentações desde então.

---

## 8. Referências Pendentes

### Cenário

`REFUND` ou `ROLLBACK` chegam antes da transação de referência estar disponível.

### Tratamento

1. A operação é registrada com status `PENDING_REFERENCE` em uma transação SQL atômica (junto com o evento `WagerTransactionPendingReference` na outbox).
2. O campo `retry_after` recebe o instante do próximo retry com backoff exponencial.
3. Um **worker stateless** (`PendingReferenceWorker`) consulta periodicamente transações com `status = 'PENDING_REFERENCE' AND retry_after <= NOW()`.
4. A cada tentativa, o worker busca a referência por `(providerId, referenceExternalTransactionId)`.

### Política de retry

| Parâmetro | Valor padrão | Env var |
|---|---|---|
| Máximo de tentativas | 10 | `PENDING_REF_MAX_RETRIES` |
| Backoff inicial | 5s | `PENDING_REF_INITIAL_BACKOFF` |
| Estratégia | Exponencial com jitter | — |
| Ao esgotar | `REJECTED` com `failurCode: REFERENCE_NOT_FOUND` | — |

### Comportamento quando a referência existe mas está pendente

- Se a referência existe mas tem status `PENDING` ou `PENDING_REFERENCE`, o worker aguarda (não resolve). A referência precisa estar `PROCESSED` para ser usada.
- Se a referência terminou com `REJECTED` ou `FAILED`, a operação dependente é imediatamente `REJECTED` com `failureCode: REFERENCE_NOT_PROCESSABLE`.

---

## 9. Reversões (REFUND e ROLLBACK)

### REFUND

- Tipo: crédito do valor integral da `BET` referenciada.
- Requer `referenceExternalTransactionId` apontando para uma `BET PROCESSED` do mesmo provedor, jogador, carteira, moeda e rodada.
- Gera ledger entry de `CREDIT`.

### ROLLBACK

- Tipo: movimento contrário ao da transação referenciada.
  - Referência `BET` → debita (estorna o crédito do jogador que havia apostado). _Nota: BET é débito do jogador, logo ROLLBACK de BET é crédito._
  - Referência `WIN` → débito.
  - Referência `REFUND` → débito.
- Requer `referenceExternalTransactionId` da transação a ser desfeita.

### Proteção contra reversão dupla

A unicidade é garantida no banco: uma segunda reversão do mesmo tipo sobre a mesma referência viola `UNIQUE (provider_id, external_transaction_id)` ou é detectada na consulta da referência como já revertida.

**Comportamento de combinações REFUND + ROLLBACK sobre a mesma aposta:**

| Situação | Comportamento |
|---|---|
| REFUND processado → ROLLBACK chega | ROLLBACK debita o valor do REFUND (estorna o crédito) |
| ROLLBACK processado → REFUND chega | REFUND é rejeitado: referência (BET) já foi revertida; `failureCode: REFERENCE_ALREADY_REVERSED` |
| Dois REFUNDs | O segundo é rejeitado por unicidade ou por referência já revertida |
| Dois ROLLBACKs | Idem |

### Reversão com saldo insuficiente

Se um `ROLLBACK` de `WIN` tentasse debitar mais que o saldo disponível, ele é **rejeitado** com `failureCode: ROLLBACK_INSUFFICIENT_FUNDS` — distinto do `INSUFFICIENT_FUNDS` de uma aposta sem saldo. O evento `WagerTransactionRejected` é publicado via outbox.

---

## 10. Inbox e Outbox

### Inbox (deduplicação de mensagens SQS)

```sql
CREATE TABLE inbox_messages (
  consumer_name  TEXT,  -- ex: "wager-transactions-consumer"
  message_id     TEXT,  -- messageId do envelope SQS
  payload_hash   TEXT,  -- hash do body; detecta reentrega com corpo alterado
  received_at    TIMESTAMPTZ,
  processed_at   TIMESTAMPTZ,
  UNIQUE (consumer_name, message_id)
)
```

O registro da inbox é inserido **dentro da mesma transação** das alterações de domínio. Assim, se a transação falhar, o registro de inbox também é revertido — sem risco de "duplamente marcado como processado" ou "processado sem registro".

A mensagem SQS é **removida da fila somente após o commit da transação**.

### Outbox (publicação confiável de eventos)

```sql
CREATE TABLE outbox_events (
  id               UUID,       -- eventId estável; preservado em republicações
  event_type       TEXT,       -- ex: "WagerTransactionProcessed"
  aggregate_id     UUID,
  aggregate_type   TEXT,
  correlation_id   UUID,
  causation_id     UUID,
  payload          JSONB,      -- snapshot imutável no momento da criação
  occurred_at      TIMESTAMPTZ,
  version          INT,
  published_at     TIMESTAMPTZ,
  attempts         INT,
  next_attempt_at  TIMESTAMPTZ,
  last_error       TEXT
)
```

Eventos são inseridos **dentro da mesma transação** das alterações de domínio. Um worker separado (`OutboxWorker`) publica os registros pendentes:

```sql
-- Múltiplos publishers sem disputa de registro
SELECT * FROM outbox_events
WHERE published_at IS NULL AND next_attempt_at <= NOW()
FOR UPDATE SKIP LOCKED
LIMIT $batchSize
```

Após publicação bem-sucedida no SQS, o worker atualiza `published_at`. Se o processo for interrompido entre a publicação e o `published_at`, outro worker assume o registro (idempotência de publicação garantida pelo `eventId` estável).

### Eventos exigidos

| Evento | Gatilho |
|---|---|
| `WagerTransactionProcessed` | Conclusão bem-sucedida (incluindo LOSS) |
| `WagerTransactionRejected` | Rejeição definitiva por regra de negócio |
| `WalletBalanceChanged` | Alteração efetiva do saldo |
| `WagerTransactionPendingReference` | Registro de espera por referência |

---

## 11. Autenticação e Autorização

### IdP: Keycloak 24

**Por que Keycloak?**
- Suporte nativo a OAuth 2.0 / OIDC com `client_credentials`.
- Auto-import de realm via JSON no startup — ambiente reproduzível sem scripts manuais.
- Amplamente utilizado em ambientes corporativos; familiar para avaliadores.

### Fluxo

1. Provider obtém token via `POST /realms/desafio/protocol/openid-connect/token` com `grant_type=client_credentials`.
2. Token JWT é enviado em `Authorization: Bearer <token>`.
3. O middleware valida assinatura via **JWKS** (busca pública do Keycloak), sem segredo compartilhado no serviço.
4. Claims extraídos: `sub` (clientId do provider), `realm_access.roles`.

### Identidade e autorização

O `providerId` autorizado é derivado do `sub` (clientId) do token JWT. **Não** é aceito do corpo da requisição sem validação.

| Regra | Implementação |
|---|---|
| Provider acessa apenas suas transações | Filtro `WHERE provider_id = $claimProviderId` em todas as queries |
| Operações internas restritas ao serviço | Role `internal` obrigatória para endpoints de carteira |
| Operações externas restritas a providers | Role `provider` obrigatória para `/wagering/transactions` |
| Sem efeito financeiro em acesso não autorizado | Middleware rejeita antes de qualquer caso de uso |

### Clientes configurados no Keycloak

| ClientId | Role | Secret (local) |
|---|---|---|
| `provider-a` | `provider` | `provider-a-secret` |
| `provider-b` | `provider` | `provider-b-secret` |
| `desafio-internal` | `internal` | `internal-secret` |

> ⚠️ Os secrets acima são **exclusivos para ambiente local de desenvolvimento**. Em produção, usar rotação via vault.

### SQS e autorização

O consumidor SQS usa credenciais AWS (simuladas via LocalStack). A autorização de domínio (provider vs. operação) é aplicada no caso de uso, garantindo as mesmas validações da entrada HTTP.

---

## 12. Composição com Uber Fx

### Princípio

O **domínio não importa Fx**. Fx é confinado à camada de composição (`internal/fx/`) e ao `cmd/server/main.go`.

### Organização por `fx.Module`

```
fx.Module("config",   ...)   // config.Load
fx.Module("database", ...)   // pgxpool, migrate runner
fx.Module("auth",     ...)   // JWKS validator
fx.Module("sqs",      ...)   // SQS client, consumer worker
fx.Module("repos",    ...)   // WalletRepo, TransactionRepo, LedgerRepo, InboxRepo, OutboxRepo
fx.Module("usecases", ...)   // OpenWallet, ProcessWager, Reconcile
fx.Module("http",     ...)   // chi router, handlers, middlewares
fx.Module("workers",  ...)   // OutboxWorker, PendingReferenceWorker
```

### Ciclo de vida (`fx.Lifecycle`)

Cada componente registra hooks `OnStart` / `OnStop`:

- **Database:** abre pool `OnStart`, fecha `OnStop` após workers pararem.
- **HTTP Server:** `ListenAndServe` em goroutine `OnStart`; `Shutdown` com context de deadline `OnStop`.
- **SQS Consumer:** inicia loop de polling `OnStart`; para de buscar e aguarda processamento em andamento `OnStop`.
- **Outbox Worker:** idem.
- **PendingReference Worker:** idem.

---

## 13. Shutdown Gracioso

Ao receber `SIGTERM` ou `SIGINT`:

1. **HTTP Server** para de aceitar novas conexões (`http.Server.Shutdown`).
2. **SQS Consumer** para de buscar (`ReceiveMessage`) e aguarda até `SHUTDOWN_TIMEOUT` pelo processamento da mensagem em andamento. Se exceder o prazo, libera visibilidade da mensagem para reentrega.
3. **Outbox/PendingRef Workers** concluem o batch atual ou abortam com timeout.
4. **Pool de banco** fecha após todos os workers pararem.

O prazo total de shutdown é configurável via `SHUTDOWN_TIMEOUT` (padrão: 30s).

---

## 14. Observabilidade

### Logs (JSON estruturado via `slog`)

Campos padrão em cada log de operação:

```json
{
  "time": "2026-09-23T10:00:00Z",
  "level": "INFO",
  "msg": "transaction processed",
  "correlationId": "...",
  "transactionId": "...",
  "walletId": "...",
  "providerId": "provider-a",
  "kind": "BET",
  "status": "PROCESSED"
}
```

**Nunca logados:** credenciais, tokens JWT, valores monetários completos de payload, dados pessoais do jogador.

### Métricas (Prometheus)

| Métrica | Tipo | Labels |
|---|---|---|
| `wager_transactions_total` | Counter | `kind`, `status` |
| `wager_transactions_duplicates_total` | Counter | `kind` |
| `wager_transaction_duration_seconds` | Histogram | `kind` |
| `outbox_pending_events` | Gauge | — |
| `outbox_publish_delay_seconds` | Histogram | `event_type` |
| `outbox_retries_total` | Counter | `event_type` |
| `sqs_messages_dlq_total` | Counter | — |
| `concurrency_conflicts_total` | Counter | `entity` |
| `reconciliation_divergences_total` | Counter | — |

### Health Checks

- `GET /health/live` — liveness (processo vivo).
- `GET /health/ready` — readiness (PostgreSQL e SQS respondem).

---

## 15. Limitações, Interpretações e Trabalho Pendente

### Interpretações adotadas

- **Moedas suportadas:** apenas `BRL`, `USD` e `EUR` no enum. O desafio permite operar somente em BRL nos cenários principais, desde que o tipo carregue a moeda — esta solução faz isso.
- **`LOSS` com valor zero:** aceito explicitamente. Produz `WagerTransactionProcessed` mas não `WalletBalanceChanged` nem lançamento no ledger.
- **`OPENING` com saldo zero:** não cria `WagerTransaction` nem ledger. A carteira é criada com `balance = 0` e `version = 1`.
- **`MessageDeduplicationId` no SQS FIFO:** será o `SHA-256` do `externalTransactionId + providerId`, garantindo deduplicação de infra além da deduplicação de aplicação.
- **`MessageGroupId` no SQS FIFO:** será o `walletId`, preservando ordenação por carteira.
- **Saldo retornado no replay:** campo `result_balance_amount` / `result_balance_currency` gravado junto com a transação no processamento original.

### Limitações conhecidas

- **Partidas dobradas (double-entry ledger):** opcional pelo desafio; não implementado nesta versão.
- **Tracing OpenTelemetry:** diferencial opcional; não implementado nesta versão.
- **Testes de carga:** diferenciais opcionais; não implementados no prazo de 3 dias.
- **Moedas além de BRL/USD/EUR:** o enum pode ser expandido; requer nova migration.
- **JWKS cache:** o validador JWT faz cache das chaves públicas do Keycloak com TTL configurável para evitar fetch a cada requisição. Rotação de chaves exige reinicialização ou cache invalidation manual nesta versão.

### Trabalho pendente (fases futuras)

- [ ] Fase 2: Domínio (`Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry`)
- [ ] Fase 3: Repositórios pgx
- [ ] Fase 4: Casos de uso
- [ ] Fase 5: HTTP handlers + middlewares de auth
- [ ] Fase 6: SQS Consumer
- [ ] Fase 7: Outbox Worker
- [ ] Fase 8: Composição Fx completa
- [ ] Fase 9: Observabilidade completa
- [ ] Fase 10: Suite de testes
