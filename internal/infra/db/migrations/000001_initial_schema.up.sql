-- migrate:up

-- Extensões
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Enum de moeda (ISO 4217 — restrito ao escopo do desafio)
CREATE TYPE currency_code AS ENUM ('BRL', 'USD', 'EUR');

-- Enum de tipos de transação
CREATE TYPE transaction_kind AS ENUM (
  'OPENING',
  'BET',
  'WIN',
  'LOSS',
  'REFUND',
  'ROLLBACK'
);

-- Enum de estados de transação
CREATE TYPE transaction_status AS ENUM (
  'PENDING',
  'PENDING_REFERENCE',
  'PROCESSED',
  'REJECTED',
  'FAILED'
);

-- Enum de direção do ledger
CREATE TYPE ledger_direction AS ENUM ('DEBIT', 'CREDIT');

-- Enum de direção de estado do inbox/outbox
CREATE TYPE outbox_event_status AS ENUM ('PENDING', 'PUBLISHED', 'FAILED');

-- ============================================================
-- WALLETS
-- ============================================================
CREATE TABLE wallets (
  id              UUID        NOT NULL DEFAULT gen_random_uuid(),
  player_id       UUID        NOT NULL,
  currency        currency_code NOT NULL,
  -- Saldo em unidades mínimas (centavos para BRL)
  balance_amount  BIGINT      NOT NULL DEFAULT 0 CHECK (balance_amount >= 0),
  -- Versão para controle otimista / auditoria
  version         BIGINT      NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  CONSTRAINT wallets_pkey PRIMARY KEY (id),
  CONSTRAINT wallets_player_currency_uniq UNIQUE (player_id, currency)
);

CREATE INDEX idx_wallets_player_id ON wallets (player_id);

-- ============================================================
-- WAGER TRANSACTIONS
-- ============================================================
CREATE TABLE wager_transactions (
  id                              UUID              NOT NULL DEFAULT gen_random_uuid(),
  wallet_id                       UUID              NOT NULL,
  player_id                       UUID              NOT NULL,

  -- Campos de origem: NULL para transações internas (OPENING)
  provider_id                     TEXT,
  external_transaction_id         TEXT,
  idempotency_key                 TEXT,
  payload_hash                    TEXT,

  -- Identificação do contexto do jogo (nulo para OPENING)
  round_id                        TEXT,
  game_id                         TEXT,

  kind                            transaction_kind  NOT NULL,
  status                          transaction_status NOT NULL DEFAULT 'PENDING',

  -- Valor monetário: amount em centavos, currency em código ISO
  money_amount                    BIGINT            NOT NULL CHECK (money_amount >= 0),
  money_currency                  currency_code     NOT NULL,

  -- Referência externa (REFUND/ROLLBACK)
  reference_external_transaction_id TEXT,
  -- Referência interna resolvida (populada após lookup)
  reference_transaction_id        UUID,

  -- Resultado financeiro devolvido ao provedor (saldo na época do processamento)
  result_balance_amount           BIGINT,
  result_balance_currency         currency_code,

  -- Código de falha/rejeição estável
  failure_code                    TEXT,

  -- Controle de retry para PENDING_REFERENCE
  retry_count                     INT               NOT NULL DEFAULT 0,
  retry_after                     TIMESTAMPTZ,

  -- Metadados de rastreamento
  correlation_id                  UUID,

  created_at                      TIMESTAMPTZ       NOT NULL DEFAULT NOW(),
  updated_at                      TIMESTAMPTZ       NOT NULL DEFAULT NOW(),

  CONSTRAINT wager_transactions_pkey PRIMARY KEY (id),
  CONSTRAINT wager_transactions_wallet_fk
    FOREIGN KEY (wallet_id) REFERENCES wallets (id),

  -- Unicidade de operação externa por provedor
  CONSTRAINT wager_transactions_provider_external_uniq
    UNIQUE (provider_id, external_transaction_id),

  -- Unicidade de chave de idempotência
  CONSTRAINT wager_transactions_idempotency_key_uniq
    UNIQUE (idempotency_key),

  -- Garantia de schema: OPENING não tem campos externos
  CONSTRAINT wager_transactions_opening_no_provider
    CHECK (
      kind != 'OPENING' OR (
        provider_id IS NULL AND
        external_transaction_id IS NULL AND
        idempotency_key IS NULL AND
        payload_hash IS NULL AND
        round_id IS NULL AND
        game_id IS NULL AND
        reference_external_transaction_id IS NULL
      )
    ),

  -- Garantia de schema: não-OPENING tem provider e external id
  CONSTRAINT wager_transactions_external_has_provider
    CHECK (
      kind = 'OPENING' OR (
        provider_id IS NOT NULL AND
        external_transaction_id IS NOT NULL
      )
    )
);

CREATE INDEX idx_wager_transactions_wallet_id     ON wager_transactions (wallet_id);
CREATE INDEX idx_wager_transactions_player_id     ON wager_transactions (player_id);
CREATE INDEX idx_wager_transactions_provider      ON wager_transactions (provider_id, external_transaction_id);
CREATE INDEX idx_wager_transactions_idempotency   ON wager_transactions (idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX idx_wager_transactions_status        ON wager_transactions (status) WHERE status IN ('PENDING', 'PENDING_REFERENCE');
CREATE INDEX idx_wager_transactions_retry_after   ON wager_transactions (retry_after) WHERE status = 'PENDING_REFERENCE';

-- ============================================================
-- WALLET LEDGER ENTRIES
-- ============================================================
CREATE TABLE wallet_ledger_entries (
  id              UUID          NOT NULL DEFAULT gen_random_uuid(),
  wallet_id       UUID          NOT NULL,
  transaction_id  UUID          NOT NULL,
  direction       ledger_direction NOT NULL,

  -- Valores em centavos
  money_amount    BIGINT        NOT NULL CHECK (money_amount > 0),
  money_currency  currency_code NOT NULL,
  balance_before  BIGINT        NOT NULL CHECK (balance_before >= 0),
  balance_after   BIGINT        NOT NULL CHECK (balance_after >= 0),

  created_at      TIMESTAMPTZ   NOT NULL DEFAULT NOW(),

  CONSTRAINT wallet_ledger_entries_pkey PRIMARY KEY (id),
  CONSTRAINT wallet_ledger_entries_wallet_fk
    FOREIGN KEY (wallet_id) REFERENCES wallets (id),
  CONSTRAINT wallet_ledger_entries_transaction_fk
    FOREIGN KEY (transaction_id) REFERENCES wager_transactions (id),

  -- Uma transação gera no máximo um lançamento por carteira
  CONSTRAINT wallet_ledger_entries_wallet_transaction_uniq
    UNIQUE (wallet_id, transaction_id),

  -- Imutabilidade por constraint: balance_after deve ser coerente
  CONSTRAINT wallet_ledger_entries_balance_debit_check
    CHECK (
      direction != 'DEBIT' OR balance_after = balance_before - money_amount
    ),
  CONSTRAINT wallet_ledger_entries_balance_credit_check
    CHECK (
      direction != 'CREDIT' OR balance_after = balance_before + money_amount
    )
);

CREATE INDEX idx_wallet_ledger_entries_wallet_id ON wallet_ledger_entries (wallet_id, created_at);
CREATE INDEX idx_wallet_ledger_entries_txn_id    ON wallet_ledger_entries (transaction_id);

-- Proibir UPDATE e DELETE no ledger via rule
CREATE RULE wallet_ledger_no_update AS ON UPDATE TO wallet_ledger_entries DO INSTEAD NOTHING;
CREATE RULE wallet_ledger_no_delete AS ON DELETE TO wallet_ledger_entries DO INSTEAD NOTHING;

-- ============================================================
-- INBOX (deduplicação de mensagens SQS)
-- ============================================================
CREATE TABLE inbox_messages (
  id              UUID        NOT NULL DEFAULT gen_random_uuid(),
  consumer_name   TEXT        NOT NULL,
  message_id      TEXT        NOT NULL,
  payload_hash    TEXT        NOT NULL,
  received_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  processed_at    TIMESTAMPTZ,

  CONSTRAINT inbox_messages_pkey PRIMARY KEY (id),
  CONSTRAINT inbox_messages_consumer_message_uniq
    UNIQUE (consumer_name, message_id)
);

-- ============================================================
-- OUTBOX (transactional outbox para eventos de integração)
-- ============================================================
CREATE TABLE outbox_events (
  id                UUID        NOT NULL,
  event_type        TEXT        NOT NULL,
  aggregate_id      UUID        NOT NULL,
  aggregate_type    TEXT        NOT NULL,
  correlation_id    UUID,
  causation_id      UUID,
  payload           JSONB       NOT NULL,
  occurred_at       TIMESTAMPTZ NOT NULL,
  version           INT         NOT NULL DEFAULT 1,

  -- Controle de publicação
  published_at      TIMESTAMPTZ,
  attempts          INT         NOT NULL DEFAULT 0,
  next_attempt_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_error        TEXT,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  CONSTRAINT outbox_events_pkey PRIMARY KEY (id)
);

CREATE INDEX idx_outbox_events_pending ON outbox_events (next_attempt_at)
  WHERE published_at IS NULL;

