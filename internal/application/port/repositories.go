package port

import (
	"context"

	"github.com/felipecristiano/desafio/internal/domain/inbox"
	"github.com/felipecristiano/desafio/internal/domain/ledger"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/outbox"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/domain/wallet"
	"github.com/google/uuid"
)

type WalletRepository interface {
	Create(ctx context.Context, tx DBTX, w *wallet.Wallet) error
	GetByID(ctx context.Context, tx DBTX, id uuid.UUID) (*wallet.Wallet, error)
	GetByIDForUpdate(ctx context.Context, tx DBTX, id uuid.UUID) (*wallet.Wallet, error)
	GetByPlayerAndCurrency(ctx context.Context, tx DBTX, playerID uuid.UUID, currency money.Currency) (*wallet.Wallet, error)
	Update(ctx context.Context, tx DBTX, w *wallet.Wallet) error
}

type WagerTransactionRepository interface {
	Create(ctx context.Context, tx DBTX, t *transaction.WagerTransaction) error
	GetByID(ctx context.Context, tx DBTX, id uuid.UUID) (*transaction.WagerTransaction, error)
	GetByIdempotencyKey(ctx context.Context, tx DBTX, key string) (*transaction.WagerTransaction, error)
	GetByProviderAndExternalID(ctx context.Context, tx DBTX, providerID, externalID string) (*transaction.WagerTransaction, error)
	Update(ctx context.Context, tx DBTX, t *transaction.WagerTransaction) error
	GetPendingReferences(ctx context.Context, tx DBTX, limit int) ([]*transaction.WagerTransaction, error)
}

type LedgerRepository interface {
	Create(ctx context.Context, tx DBTX, entry *ledger.WalletLedgerEntry) error
	GetByWalletID(ctx context.Context, tx DBTX, walletID uuid.UUID, cursor *uuid.UUID, limit int) ([]*ledger.WalletLedgerEntry, error)
	CalculateBalance(ctx context.Context, tx DBTX, walletID uuid.UUID) (money.Money, int, error)
}

type InboxRepository interface {
	CreateIfAbsent(ctx context.Context, tx DBTX, msg *inbox.InboxMessage) (bool, error)
	MarkProcessed(ctx context.Context, tx DBTX, consumerName, messageID string) error
}

type OutboxRepository interface {
	Create(ctx context.Context, tx DBTX, event *outbox.OutboxEvent) error
	FetchPendingForPublish(ctx context.Context, tx DBTX, batchSize int) ([]*outbox.OutboxEvent, error)
	MarkPublished(ctx context.Context, tx DBTX, id uuid.UUID) error
	RecordFailure(ctx context.Context, tx DBTX, id uuid.UUID, lastErr string, nextAttemptAfterSecs int) error
}
