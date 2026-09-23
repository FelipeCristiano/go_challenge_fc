# ARCHITECTURE.md — Processamento Distribuído de Apostas em Go

Este documento detalha as decisões de engenharia, arquitetura, modelo de dados, tratamento de concorrência e resiliência adotados para atender a todos os requisitos do desafio.

---

## 1. Visão Geral e Arquitetura

O sistema implementa uma arquitetura distribuída resiliente a falhas e orientada ao domínio (DDD / Ports & Adapters / Clean Architecture), composta por:
- **API HTTP**: Exposição dos contratos REST para abertura de carteira, envio de apostas, consulta e reconciliação.
- **Consumidor SQS FIFO**: Entrada assíncrona com garantias estritas de ordenação (`MessageGroupId` por carteira) e idempotência.
- **PostgreSQL**: Fonte única da verdade contendo o saldo, ledger append-only e tabelas de controle de mensageria (`inbox_messages` e `outbox_events`).
- **IdP Externo (Keycloak)**: Provedor OAuth 2.0 / OIDC para autenticação via `client_credentials`.
- **Uber Fx**: Motor de injeção de dependência e gerenciamento do ciclo de vida da aplicação.

```
                  ┌───────────────────────────────┐
                  │      Keycloak (OAuth 2.0)     │
                  └──────────────┬────────────────┘
                                 │ JWKS / Bearer Token
                                 ▼
┌──────────────────┐       ┌─────────────┐       ┌──────────────────────┐
│ Provedor Externo │──────▶│  API HTTP   │       │ Consumidor SQS FIFO  │
│    (HTTP API)    │       │   (chi)     │       │     (LocalStack)     │
└──────────────────┘       └──────┬──────┘       └──────────┬───────────┘
                                  │                         │
                                  ▼                         ▼
                       ┌────────────────────────────────────────┐
                       │           Application Layer            │
                       │               (Use Cases)              │
                       └──────────────────┬─────────────────────┘
                                          │ UnitOfWork (Tx)
                                          ▼
                       ┌────────────────────────────────────────┐
                       │          Domain Model / Entity         │
                       │     Wallet / Transaction / Ledger      │
                       └──────────────────┬─────────────────────┘
                                          │
                                          ▼
                       ┌────────────────────────────────────────┐
                       │           PostgreSQL (pgx/v5)          │
                       │  wallets (SELECT FOR UPDATE)           │
                       │  wager_transactions (Idempotency Key)  │
                       │  wallet_ledger_entries (Append-only)   │
                       │  inbox_messages (Deduplicação)         │
                       │  outbox_events (SKIP LOCKED)           │
                       └────────────────────────────────────────┘
```

---

## 2. Escolha do IdP, Validação de Credenciais e Modelo de Permissões

### 2.1. Escolha do IdP
- **Tecnologia**: **Keycloak 24**, executado via Docker Compose.
- **Justificativa**: O Keycloak é um IdP de padrão aberto amplamente adotado na indústria, compatível com as especificações OAuth 2.0 e OpenID Connect (OIDC). Oferece suporte nativo ao fluxo `client_credentials` para autenticação máquina-a-máquina (M2M) entre os provedores de jogos e o backend, além de permitir o provisionamento declarativo via importação automática de realm (`desafio-realm.json`), viabilizando inicialização limpa e determinística nos testes e ambientes locais.
- **Fora do escopo**: Cadastro de usuários/senhas e emissão própria de tokens JWT são delegados inteiramente ao IdP.

### 2.2. Validação de Credenciais e Cache JWKS
- O serviço backend não compartilha credenciais secretas para validar tokens: a verificação de autenticidade dos tokens JWT é realizada de forma assimétrica e sem estado através do endpoint público **JWKS (JSON Web Key Set)** (`/protocol/openid-connect/certs`).
- **Cache em Memória com `sync.RWMutex`**: Para evitar sobrecarga de chamadas de rede ao Keycloak a cada requisição HTTP, o validador (`KeycloakValidator`) mantém um cache das chaves públicas RSA indexadas por `kid` com TTL de 15 minutos e suporte a concorrência via double-checked locking (`sync.RWMutex`).
- O middleware de autenticação valida assinatura, expiração (`exp`), emissor (`iss`) e audiência antes de repassar a requisição aos handlers de negócio.

### 2.3. Modelo de Permissões e Isolamento de Provedores
- **Papéis (Roles)**:
  - `internal`: Destinado a operações internas e de infraestrutura administrativa (ex.: `POST /wallets` e `POST /wallets/:walletId/reconciliation`).
  - `provider`: Destinado aos provedores externos de jogos para submissão de transações (`POST /wagering/transactions`) e consultas.
- **Isolamento de Tenants (Provedores)**:
  - O `providerId` autorizado é extraído diretamente da claim de identidade do token (`sub` / `clientId`).
  - Um provedor (`provider-a`) é rigorosamente impedido de consultar, processar apostas ou executar replays em nome de outro (`provider-b`).
  - Qualquer discrepância entre a identidade autenticada e o `providerId` da requisição é rejeitada no middleware ou no caso de uso com erro de autorização imediato, sem efeitos colaterais no banco de dados.

---

## 3. Dinheiro e Mapeamento de `Money`

### 3.1. Representação e Eliminação de Ponto Flutuante
- **Proibição absoluta de `float32` e `float64`**: Números em ponto flutuante introduzem imprecisões binárias inaceitáveis em cálculos financeiros.
- **Representação Interna**: O Value Object `Money` utiliza **`int64` em unidades mínimas (centavos)**, associado à moeda (`ISO 4217`).
  - Exemplo: `R$ 25.00` é representado como `2500` centavos na moeda `BRL`.
- **Limites e Proteção contra Overflow**:
  - `int64` suporta valores até `9.223.372.036.854.775.807` centavos (~92 trilhões de BRL), suficiente para qualquer volume financeiro.
  - As operações `Add`, `Sub` e `Neg` validam overflow/underflow em tempo de execução com erro explícito.

### 3.2. Contrato Externo e Parsing Estrito
- O contrato de entrada e saída utiliza strings decimais formatadas:
  ```json
  { "amount": "25.00", "currency": "BRL" }
  ```
- O parser de entrada externa (`NewFromExternalString`):
  - Exige exatamente duas casas decimais após o ponto (`.`).
  - Rejeita valores nulos, vazios, notação científica (`1e5`), caracteres alfanuméricos e palavras reservadas (`NaN`, `Infinity`).
  - Rejeita quantias negativas em entradas externas (quantias negativas só existem como resultados intermediários de diferenças internas).

### 3.3. Persistência no PostgreSQL
- No banco de dados, o dinheiro é persistido diretamente como:
  ```sql
  money_amount   BIGINT        NOT NULL CHECK (money_amount >= 0),
  money_currency currency_code NOT NULL
  ```
- O mapeamento entre o Go `int64` e o PostgreSQL `BIGINT` é direto, nativo e exato, sem conversão intermediária ou risco de truncamento.

---

## 4. Transações SQL e Delimitação entre Repositórios

### 4.1. Biblioteca de Acesso ao Banco: `pgx/v5`
- **Escolha**: Driver nativo **`jackc/pgx/v5`** com SQL explícito.
- **Justificativa**: `pgx` é o driver de maior performance e menor consumo de memória para PostgreSQL em Go. O uso de SQL explícito torna locks, transações e restrições totalmente auditáveis e transparentes, eliminando a opacidade e os riscos de geração de queries ineficientes de ORMs.

### 4.2. Delimitação Transacional com Unit of Work
- A integridade financeira é mantida por meio do padrão **Unit of Work** (`uow.WithTx`), garantindo que **uma operação financeira equivale a uma única transação SQL atômica**.
- Os repositórios recebem a interface `port.DBTX`, podendo operar com `*pgxpool.Pool` ou `pgx.Tx`.

### 4.3. Fronteira da Transação Atômica
Todas as seguintes mutações ocorrem atomicamente em um único `BEGIN ... COMMIT`:
1. Inserção na `inbox_messages` (se a mensagem originou-se do SQS FIFO).
2. Lock pessimista da carteira: `SELECT ... FROM wallets WHERE id = $1 FOR UPDATE`.
3. Verificação e débito/crédito do saldo em memória e persistência em `wallets` (com incremento de versão).
4. Persistência da `wager_transactions` (armazenando status, payload hash e saldo da época).
5. Inserção do lançamento imutável em `wallet_ledger_entries`.
6. Enfileiramento do evento na tabela `outbox_events` (Transactional Outbox).

Se ocorrer falha em qualquer etapa (ou queda abrupta de energia), o PostgreSQL realiza o rollback automático de todas as alterações, impedindo estados inconsistentes.

---

## 5. Controle de Concorrência e Bloqueios (Locks)

### 5.1. Estratégia Adotada: Locking Pessimista Granular por Carteira
- Em cada movimentação de saldo, executa-se:
  ```sql
  SELECT id, player_id, currency, balance_amount, version, created_at, updated_at
  FROM wallets
  WHERE id = $1
  FOR UPDATE;
  ```
- **Por que não Optimistic Concurrency Control (OCC) com retry?**
  Em cenários de alta disputa na mesma carteira (como jogos rápidos), retentativas otimistas causam tempestades de contenção (*retry storms*), desperdício de CPU e aumentam a latência da cauda (p99).
- **Paralelismo Real**: O lock é estritamente granular no registro da carteira (`WHERE id = $1`). Carteiras diferentes de jogadores distintos executam 100% em paralelo, sendo terminantemente proibido qualquer lock global na aplicação.

### 5.2. Cenário Concorrente Obrigatório (2 apostas de 80.00 sobre saldo de 100.00)
- Três ou mais instâncias concorrentes recebendo operações na mesma carteira são serializadas pela fila do lock `FOR UPDATE` do PostgreSQL.
- A primeira requisição adquire o lock, debita `80.00`, atualiza a carteira para `20.00` e commita.
- A segunda requisição obtém o lock, lê o saldo atualizado de `20.00`, detecta fundos insuficientes, não altera o saldo, registra a aposta como `REJECTED` (`failureCode: INSUFFICIENT_FUNDS`) e commita.
- O saldo final permanece `20.00 BRL` e o ledger registra exatamente 1 débito.

---

## 6. Idempotência e Reprodução do Resultado Original

A solução implementa idempotência durável em 3 níveis:

1. **Unicidade no Banco de Dados**:
   - `UNIQUE (idempotency_key)`: Garante que nenhuma operação com a mesma chave seja processada duas vezes.
   - `UNIQUE (provider_id, external_transaction_id)`: Impede que a mesma transação externa seja cadastrada sob chaves de idempotência diferentes.
2. **Hash Canônico de Payload (`CanonicalPayloadHash`)**:
   - É gerado o SHA-256 de um JSON ordenado lexicograficamente com os campos de negócio (`providerId`, `externalTransactionId`, `playerId`, `walletId`, `roundId`, `gameId`, `kind`, `money`).
   - Garante que a representação seja estritamente determinística tanto para requisições vindas da **API HTTP** quanto para mensagens consumidas do **SQS FIFO**, independentemente da ordem em que os campos foram enviados pelo cliente.
   - Metadados de transporte (`Idempotency-Key`, `X-Correlation-ID`, headers SQS) são excluídos do cálculo do hash.
   - Se uma chave existente for reenviada com payload diferente, o sistema retorna `409 Conflict` (`ErrPayloadConflict`).
3. **Replay Idempotente com Saldo Histórico**:
   - Ao detectar reenvio de operação já processada (`PROCESSED`), a aplicação devolve o snapshot exato gravado em `result_balance_amount` da transação original, mesmo que a carteira tenha tido outras movimentações posteriores.
   - O campo `idempotentReplay: true` é retornado com status `200 OK`.

### 6.1. Mapeamento de Códigos de Status HTTP

| Código | Cenário de Resposta |
|---|---|
| **`200 OK`** | Operação financeira processada com sucesso (`PROCESSED`) ou Replay Idempotente (`idempotentReplay: true`). |
| **`201 Created`** | Carteira aberta com sucesso (`POST /wallets`). |
| **`202 Accepted`** | Reversão criada em estado de espera (`PENDING_REFERENCE`). |
| **`400 Bad Request`** | Erro de parsing em `Money` (ex.: float, notação científica), campos obrigatórios ausentes ou payload inválido. |
| **`401 Unauthorized`** | Cabeçalho `Authorization` ausente, token malformado, expirado ou com assinatura inválida pelo JWKS. |
| **`403 Forbidden`** | Tenant mismatch (token de `provider-a` tentando operar em nome de `provider-b`), ausência do papel necessário (`internal`) ou tentativa externa de submeter transação `OPENING`. |
| **`404 Not Found`** | Carteira ou transação inexistente. |
| **`409 Conflict`** | Conflito de payload para a mesma chave de idempotência (`ErrPayloadConflict`) ou tentativa de criar carteira duplicada para o mesmo jogador e moeda (`ErrWalletAlreadyExists`). |

---

## 7. Referências Pendentes e Reversões

### 7.1. Referências Pendentes (`PENDING_REFERENCE`)
- Se uma operação `REFUND` ou `ROLLBACK` for recebida antes da transação original que ela referencia, a aplicação:
  1. Cria a transação em estado `PENDING_REFERENCE`.
  2. Publica o evento `WagerTransactionPendingReference` na outbox.
  3. Define o campo `retry_after` com backoff exponencial.
- Um worker em background (`PendingReferenceWorker`) busca transações com `status = 'PENDING_REFERENCE' AND retry_after <= NOW()`, tentando resolver a referência.
- Ao atingir o limite máximo de tentativas (`PENDING_REF_MAX_RETRIES`), a transação é finalizada como `REJECTED` com `failureCode: REFERENCE_NOT_FOUND`.

### 7.2. Regras de Reversão (`REFUND` e `ROLLBACK`)
- **`REFUND`**: Devolve integralmente o valor de uma `BET` processada na mesma rodada (gera crédito).
- **`ROLLBACK`**: Inverte o movimento financeiro da transação referenciada:
  - Rollback de `BET` (débito) gera crédito.
  - Rollback de `WIN` (crédito) gera débito.
  - Rollback de `REFUND` (crédito) gera débito.
- **Rollback com Saldo Insuficiente**: Se um Rollback de crédito precisar debitar um saldo que o jogador já sacou/gastou, ele é rejeitado e auditado com o código `ROLLBACK_INSUFFICIENT_FUNDS` (distinto de `INSUFFICIENT_FUNDS` de apostas normais).
- **Prevenção de Dupla Reversão**: Uma referência só aceita uma reversão bem-sucedida de cada tipo. Combinações conflitantes são rejeitadas com `REFERENCE_ALREADY_REVERSED`.

---

## 8. Inbox e Outbox Patterns

### 8.1. Inbox Pattern e Consumo SQS FIFO
- A tabela `inbox_messages` possui restrição única `UNIQUE (consumer_name, message_id)`.
- No consumo do SQS, a inserção na inbox ocorre dentro da mesma transação do domínio (`port.UnitOfWork`).
- Mensagens reentregues pelo broker com o mesmo `messageId` são identificadas e não duplicam operações financeiras.
- **Remoção Pós-Commit (`DeleteMessage`)**: A exclusão da mensagem na fila SQS só é invocada **após** a conclusão e o commit bem-sucedido de toda a transação no PostgreSQL. Caso o processo seja interrompido abruptamente ou a transação sofra rollback, a mensagem permanece intacta na fila e será reprocessada com segurança por outra instância.
- **Liberação Imediata de Visibilidade (`ChangeMessageVisibility(0)`)**: Em caso de falhas transitórias de infraestrutura (ex.: indisponibilidade momentânea de banco), o consumidor zera o timeout de visibilidade da mensagem, disponibilizando-a imediatamente para redrive sem bloquear o pipeline até atingir o limite de DLQ (`wager-transactions-dlq.fifo`).
- **Tratamento de Poison Pills**: Mensagens com formato JSON irrecuperável ou erros de domínio irreversíveis (`PAYLOAD_CONFLICT`, `OPENING_FORBIDDEN`) são removidas da fila com log de auditoria para evitar loops infinitos.

### 8.2. Transactional Outbox Pattern (Publicador de Eventos)
- Eventos de domínio são persistidos na tabela `outbox_events` na mesma transação SQL que altera o saldo e grava o ledger.
- **Worker Multinstância**: Múltiplos processos publicadores consultam a outbox concorrentemente utilizando:
  ```sql
  SELECT id, event_type, payload
  FROM outbox_events
  WHERE published_at IS NULL AND next_attempt_at <= NOW()
  ORDER BY next_attempt_at ASC
  LIMIT 50
  FOR UPDATE SKIP LOCKED;
  ```
- O uso de `SKIP LOCKED` assegura que nenhum worker bloqueie outro e que nenhum evento seja processado simultaneamente por mais de uma réplica.
- O `eventId` gerado na criação do evento permanece imutável em retentativas, garantindo deduplicação aos consumidores downstream.

---

## 9. Composição da Aplicação com Uber Fx

A injeção de dependência e ciclo de vida são orquestrados com **`go.uber.org/fx`**, organizados em módulos desacoplados:
- `config.Module`: Leitura e validação de variáveis de ambiente.
- `postgres.Module`: Pool `pgxpool.Pool` e runner de migrations.
- `repository.Module`: Instanciação dos repositórios (`Wallet`, `Transaction`, `Ledger`, `Inbox`, `Outbox`).
- `usecase.Module`: Casos de uso da aplicação.
- `http.Module`: Servidor HTTP `net/http` com roteamento `chi` e middlewares.
- `worker.Module`: Consumidor SQS, worker de Outbox e worker de referências pendentes.

O domínio (`internal/domain`) permanece completamente limpo e independente de frameworks, sem nenhuma menção ao Uber Fx.

---

## 10. Shutdown Gracioso (Graceful Shutdown)

O encerramento ordenado é gerenciado pelos hooks de `fx.Lifecycle`:
1. **Interrupção de Entradas**: O servidor HTTP fecha portas e para de aceitar conexões (`httpServer.Shutdown`). O consumidor SQS para o polling de novas mensagens.
2. **Conclusão de Trabalho em Andamento**: Requisições em voo e mensagens em processamento têm até o prazo configurado (`SHUTDOWN_TIMEOUT`, padrão 30s) para comitar ou abortar com segurança.
3. **Liberação de Mensagens Incompletas**: Caso uma mensagem SQS não finalize dentro do prazo, seu *visibility timeout* é liberado para reentrega imediata por outra instância.
4. **Fechamento de Recursos**: Após a parada de todos os workers e servidores, o pool do PostgreSQL (`pgxpool.Close()`) é finalizado.

---

## 11. Observabilidade

- **Logs JSON Estruturados**: Implementados via `log/slog`. Cada log de transação inclui os identificadores de rastreabilidade: `correlationId`, `messageId`, `transactionId`, `walletId` e `providerId`. Nenhuma credencial, segredo ou payload financeiro bruto é exposto.
- **Métricas Prometheus** (expostas via `GET /metrics` no pacote `internal/infra/observability`):
  - `wager_transactions_total`: Contador por status (`PROCESSED`, `REJECTED`, `PENDING_REFERENCE`, `FAILED`), tipo (`BET`, `WIN`, `LOSS`, `REFUND`, `ROLLBACK`) e origem (`HTTP`, `SQS`).
  - `wager_processing_duration_seconds`: Histograma de latência de processamento particionado por tipo, origem e status.
  - `wager_transactions_duplicates_total`: Contagem de requisições duplicadas identificadas (`idempotent_replay`, `inbox_duplicate`).
  - `wager_retries_total`: Retentativas efetuadas por worker (`sqs`, `outbox`, `pending_ref`) e resultado (`success`, `failure`, `retry`, `max_reached`).
  - `wager_dlq_messages_total`: Mensagens descartadas ou direcionadas para DLQ (`poison_pill`, `terminal_domain_error`, `max_retries_exceeded`).
  - `wager_concurrency_conflicts_total`: Conflitos de concorrência ou payload conflict detectados (`process_wager`, `payload_conflict`).
  - `outbox_publish_delay_seconds`: Histograma do atraso em segundos entre a ocorrência do evento de domínio (`occurredAt`) e sua publicação no SQS FIFO.
  - `outbox_published_events_total`: Total de eventos publicados pela outbox por `event_type` e `status` (`success`, `failure`).
  - `reconciliation_checks_total`: Total de verificações de reconciliação de saldo executadas por status (`ok`, `divergent`).
  - `reconciliation_divergences_total`: Total de divergências financeiras detectadas por moeda (`currency`).
- **Health Checks**:
  - `GET /health/live`: Liveness do processo Go (status `UP`).
  - `GET /health/ready`: Readiness validando conectividade com PostgreSQL (`SELECT 1`).

---

## 12. Limitações e Interpretações Adotadas

1. **Moedas Suportadas**: O schema suporta enum `currency_code ('BRL', 'USD', 'EUR')`. A expansão para outras moedas requer atualização de migration.
2. **Partidas Dobradas**: Conforme permitido pelo desafio, foi adotado ledger append-only granular por carteira, dispensando partidas dobradas globais.
3. **Cache de Chaves JWKS**: O middleware mantém cache em memória das chaves públicas do Keycloak para evitar chamadas de rede repetidas por requisição.
4. **Trabalho Concluído**:
   - [x] Fase 1: Infraestrutura (Docker Compose, PostgreSQL, LocalStack, Keycloak, Migrations)
   - [x] Fase 2: Modelo de Domínio (`Money`, `Wallet`, `WagerTransaction`, `WalletLedgerEntry`, `Events`, `Inbox`, `Outbox`)
   - [x] Fase 3: Persistência (`pgx/v5`, Repositórios SQL, Unit of Work, Testes de Integração)
   - [x] Fase 4: Casos de uso (`OpenWallet`, `ProcessWager`, `ReconcileWallet`, `RetryPendingReferences`)
   - [x] Fase 5: API HTTP e Handlers (`chi`, autenticação OIDC/JWKS, autorização por role/providerId, health checks)
   - [x] Fase 6: Consumidor SQS (Worker FIFO assíncrono, deduplicação Inbox, remoção pós-commit, liberação de visibilidade)
   - [x] Fase 7: Outbox Worker (Publicador com FOR UPDATE SKIP LOCKED, backoff exponencial, deduplicação estável por eventId)
   - [x] Fase 8: Composição Uber Fx (`cmd/server/main.go`, injeção de dependência e hooks de ciclo de vida com shutdown gracioso)
   - [x] Fase 9: Observabilidade completa (Métricas Prometheus via `/metrics`, rastreabilidade JSON por `slog`, health checks)
5. **Trabalho a Seguir**:
   - [ ] Fase 10: Testes distribuídos de concorrência e recuperação