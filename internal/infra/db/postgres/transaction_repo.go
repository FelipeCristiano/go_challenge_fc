package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type WagerTransactionRepository struct{}

func NewWagerTransactionRepository() *WagerTransactionRepository {
	return &WagerTransactionRepository{}
}

func (r *WagerTransactionRepository) Create(ctx context.Context, tx port.DBTX, t *transaction.WagerTransaction) error {
	query := `
		INSERT INTO wager_transactions (
			id, wallet_id, player_id, provider_id, external_transaction_id,
			idempotency_key, payload_hash, round_id, game_id, kind, status,
			money_amount, money_currency, reference_external_transaction_id,
			reference_transaction_id, result_balance_amount, result_balance_currency,
			failure_code, retry_count, retry_after, correlation_id, created_at, updated_at
		) VALUES (
			$1, $2, $3, $4, $5,
			$6, $7, $8, $9, $10, $11,
			$12, $13, $14,
			$15, $16, $17,
			$18, $19, $20, $21, $22, $23
		)
	`

	var (
		resBalAmt *int64
		resBalCur *string
	)
	if t.ResultBalance() != nil {
		amt := t.ResultBalance().Amount()
		cur := string(t.ResultBalance().Currency())
		resBalAmt = &amt
		resBalCur = &cur
	}

	var curStr *string
	if t.Money().Currency() != "" {
		s := string(t.Money().Currency())
		curStr = &s
	}

	_, err := tx.Exec(ctx, query,
		t.ID(),
		t.WalletID(),
		t.PlayerID(),
		t.ProviderID(),
		t.ExternalTransactionID(),
		t.IdempotencyKey(),
		t.PayloadHash(),
		t.RoundID(),
		t.GameID(),
		string(t.Kind()),
		string(t.Status()),
		t.Money().Amount(),
		curStr,
		t.ReferenceExternalTransactionID(),
		t.ReferenceTransactionID(),
		resBalAmt,
		resBalCur,
		t.FailureCode(),
		t.RetryCount(),
		t.RetryAfter(),
		t.CorrelationID(),
		t.CreatedAt(),
		t.UpdatedAt(),
	)

	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique violation
			return errs.ErrDuplicateOperation
		}
		return fmt.Errorf("wager transaction repo: create: %w", err)
	}

	return nil
}

func (r *WagerTransactionRepository) GetByID(ctx context.Context, tx port.DBTX, id uuid.UUID) (*transaction.WagerTransaction, error) {
	query := r.baseSelect() + ` WHERE id = $1`
	return r.scanRow(tx.QueryRow(ctx, query, id))
}

func (r *WagerTransactionRepository) GetByIdempotencyKey(ctx context.Context, tx port.DBTX, key string) (*transaction.WagerTransaction, error) {
	query := r.baseSelect() + ` WHERE idempotency_key = $1`
	return r.scanRow(tx.QueryRow(ctx, query, key))
}

func (r *WagerTransactionRepository) GetByProviderAndExternalID(ctx context.Context, tx port.DBTX, providerID, externalID string) (*transaction.WagerTransaction, error) {
	query := r.baseSelect() + ` WHERE provider_id = $1 AND external_transaction_id = $2`
	return r.scanRow(tx.QueryRow(ctx, query, providerID, externalID))
}

func (r *WagerTransactionRepository) Update(ctx context.Context, tx port.DBTX, t *transaction.WagerTransaction) error {
	query := `
		UPDATE wager_transactions
		SET status = $1,
			reference_transaction_id = $2,
			result_balance_amount = $3,
			result_balance_currency = $4,
			failure_code = $5,
			retry_count = $6,
			retry_after = $7,
			updated_at = $8
		WHERE id = $9
	`
	var (
		resBalAmt *int64
		resBalCur *string
	)
	if t.ResultBalance() != nil {
		amt := t.ResultBalance().Amount()
		cur := string(t.ResultBalance().Currency())
		resBalAmt = &amt
		resBalCur = &cur
	}

	cmd, err := tx.Exec(ctx, query,
		string(t.Status()),
		t.ReferenceTransactionID(),
		resBalAmt,
		resBalCur,
		t.FailureCode(),
		t.RetryCount(),
		t.RetryAfter(),
		t.UpdatedAt(),
		t.ID(),
	)
	if err != nil {
		return fmt.Errorf("wager transaction repo: update: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return errs.ErrTransactionNotFound
	}
	return nil
}

func (r *WagerTransactionRepository) GetPendingReferences(ctx context.Context, tx port.DBTX, limit int) ([]*transaction.WagerTransaction, error) {
	query := r.baseSelect() + `
		WHERE status = 'PENDING_REFERENCE' AND (retry_after IS NULL OR retry_after <= NOW())
		ORDER BY retry_after ASC NULLS FIRST
		LIMIT $1
	`
	rows, err := tx.Query(ctx, query, limit)
	if err != nil {
		return nil, fmt.Errorf("wager transaction repo: get pending references: %w", err)
	}
	defer rows.Close()

	var result []*transaction.WagerTransaction
	for rows.Next() {
		t, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, t)
	}
	return result, rows.Err()
}

func (r *WagerTransactionRepository) baseSelect() string {
	return `
		SELECT
			id, wallet_id, player_id, provider_id, external_transaction_id,
			idempotency_key, payload_hash, round_id, game_id, kind, status,
			money_amount, money_currency, reference_external_transaction_id,
			reference_transaction_id, result_balance_amount, result_balance_currency,
			failure_code, retry_count, retry_after, correlation_id, created_at, updated_at
		FROM wager_transactions
	`
}

func (r *WagerTransactionRepository) scanRow(row pgx.Row) (*transaction.WagerTransaction, error) {
	t, err := r.scan(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.ErrTransactionNotFound
		}
		return nil, fmt.Errorf("wager transaction repo: scan row: %w", err)
	}
	return t, nil
}

type scannable interface {
	Scan(dest ...any) error
}

func (r *WagerTransactionRepository) scan(s scannable) (*transaction.WagerTransaction, error) {
	var (
		id                             uuid.UUID
		walletID                       uuid.UUID
		playerID                       uuid.UUID
		providerID                     *string
		externalTxnID                  *string
		idempotencyKey                 *string
		payloadHash                    *string
		roundID                        *string
		gameID                         *string
		kindStr                        string
		statusStr                      string
		moneyAmount                    int64
		moneyCurrencyStr               string
		referenceExternalTransactionID *string
		referenceTransactionID         *uuid.UUID
		resultBalanceAmount            *int64
		resultBalanceCurrencyStr       *string
		failureCode                    *string
		retryCount                     int
		retryAfter                     *time.Time
		correlationID                  *uuid.UUID
		createdAt                      time.Time
		updatedAt                      time.Time
	)

	err := s.Scan(
		&id, &walletID, &playerID, &providerID, &externalTxnID,
		&idempotencyKey, &payloadHash, &roundID, &gameID, &kindStr, &statusStr,
		&moneyAmount, &moneyCurrencyStr, &referenceExternalTransactionID,
		&referenceTransactionID, &resultBalanceAmount, &resultBalanceCurrencyStr,
		&failureCode, &retryCount, &retryAfter, &correlationID, &createdAt, &updatedAt,
	)
	if err != nil {
		return nil, err
	}

	curr := money.Currency(moneyCurrencyStr)
	m := money.NewFromInt64(moneyAmount, curr)

	var resBal *money.Money
	if resultBalanceAmount != nil && resultBalanceCurrencyStr != nil {
		b := money.NewFromInt64(*resultBalanceAmount, money.Currency(*resultBalanceCurrencyStr))
		resBal = &b
	}

	return transaction.Rehydrate(
		id, walletID, playerID,
		providerID, externalTxnID, idempotencyKey, payloadHash,
		roundID, gameID,
		transaction.Kind(kindStr),
		transaction.Status(statusStr),
		m,
		referenceExternalTransactionID,
		referenceTransactionID,
		resBal,
		failureCode,
		retryCount,
		retryAfter,
		correlationID,
		createdAt,
		updatedAt,
	), nil
}
