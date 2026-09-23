package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/wallet"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type WalletRepository struct{}

func NewWalletRepository() *WalletRepository {
	return &WalletRepository{}
}

func (r *WalletRepository) Create(ctx context.Context, tx port.DBTX, w *wallet.Wallet) error {
	query := `
		INSERT INTO wallets (id, player_id, currency, balance_amount, version, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`
	_, err := tx.Exec(ctx, query,
		w.ID(),
		w.PlayerID(),
		string(w.Currency()),
		w.Balance().Amount(),
		w.Version(),
		w.CreatedAt(),
		w.UpdatedAt(),
	)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // unique_violation
			return errs.ErrWalletAlreadyExists
		}
		return fmt.Errorf("wallet repo: create: %w", err)
	}
	return nil
}

func (r *WalletRepository) GetByID(ctx context.Context, tx port.DBTX, id uuid.UUID) (*wallet.Wallet, error) {
	query := `
		SELECT id, player_id, currency, balance_amount, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
	`
	return r.scanRow(tx.QueryRow(ctx, query, id))
}

func (r *WalletRepository) GetByIDForUpdate(ctx context.Context, tx port.DBTX, id uuid.UUID) (*wallet.Wallet, error) {
	query := `
		SELECT id, player_id, currency, balance_amount, version, created_at, updated_at
		FROM wallets
		WHERE id = $1
		FOR UPDATE
	`
	return r.scanRow(tx.QueryRow(ctx, query, id))
}

func (r *WalletRepository) GetByPlayerAndCurrency(ctx context.Context, tx port.DBTX, playerID uuid.UUID, currency money.Currency) (*wallet.Wallet, error) {
	query := `
		SELECT id, player_id, currency, balance_amount, version, created_at, updated_at
		FROM wallets
		WHERE player_id = $1 AND currency = $2
	`
	return r.scanRow(tx.QueryRow(ctx, query, playerID, string(currency)))
}

func (r *WalletRepository) Update(ctx context.Context, tx port.DBTX, w *wallet.Wallet) error {
	query := `
		UPDATE wallets
		SET balance_amount = $1, version = $2, updated_at = $3
		WHERE id = $4
	`
	cmd, err := tx.Exec(ctx, query,
		w.Balance().Amount(),
		w.Version(),
		w.UpdatedAt(),
		w.ID(),
	)
	if err != nil {
		return fmt.Errorf("wallet repo: update: %w", err)
	}
	if cmd.RowsAffected() == 0 {
		return errs.ErrWalletNotFound
	}
	return nil
}

func (r *WalletRepository) scanRow(row pgx.Row) (*wallet.Wallet, error) {
	var (
		id            uuid.UUID
		playerID      uuid.UUID
		currencyStr   string
		balanceAmount int64
		version       int64
		createdAt     time.Time
		updatedAt     time.Time
	)

	err := row.Scan(&id, &playerID, &currencyStr, &balanceAmount, &version, &createdAt, &updatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, errs.ErrWalletNotFound
		}
		return nil, fmt.Errorf("wallet repo: scan: %w", err)
	}

	curr := money.Currency(currencyStr)
	bal := money.NewFromInt64(balanceAmount, curr)

	return wallet.Rehydrate(id, playerID, curr, bal, version, createdAt, updatedAt), nil
}
