package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/ledger"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type LedgerRepository struct{}

func NewLedgerRepository() *LedgerRepository {
	return &LedgerRepository{}
}

func (r *LedgerRepository) Create(ctx context.Context, tx port.DBTX, entry *ledger.WalletLedgerEntry) error {
	query := `
		INSERT INTO wallet_ledger_entries (
			id, wallet_id, transaction_id, direction,
			money_amount, money_currency, balance_before, balance_after, created_at
		) VALUES (
			$1, $2, $3, $4,
			$5, $6, $7, $8, $9
		)
	`
	_, err := tx.Exec(ctx, query,
		entry.ID(),
		entry.WalletID(),
		entry.TransactionID(),
		string(entry.Direction()),
		entry.Money().Amount(),
		string(entry.Money().Currency()),
		entry.BalanceBefore().Amount(),
		entry.BalanceAfter().Amount(),
		entry.CreatedAt(),
	)
	if err != nil {
		return fmt.Errorf("ledger repo: create: %w", err)
	}
	return nil
}

func (r *LedgerRepository) GetByWalletID(ctx context.Context, tx port.DBTX, walletID uuid.UUID, cursor *uuid.UUID, limit int) ([]*ledger.WalletLedgerEntry, error) {
	if limit <= 0 {
		limit = 50
	}

	var rows pgx.Rows
	var err error

	if cursor != nil {
		// Busca baseada em cursor estável utilizando created_at e id
		query := `
			WITH cursor_entry AS (
				SELECT created_at, id FROM wallet_ledger_entries WHERE id = $2
			)
			SELECT
				l.id, l.wallet_id, l.transaction_id, l.direction,
				l.money_amount, l.money_currency, l.balance_before, l.balance_after, l.created_at
			FROM wallet_ledger_entries l, cursor_entry c
			WHERE l.wallet_id = $1
			  AND (l.created_at, l.id) < (c.created_at, c.id)
			ORDER BY l.created_at DESC, l.id DESC
			LIMIT $3
		`
		rows, err = tx.Query(ctx, query, walletID, *cursor, limit)
	} else {
		query := `
			SELECT
				id, wallet_id, transaction_id, direction,
				money_amount, money_currency, balance_before, balance_after, created_at
			FROM wallet_ledger_entries
			WHERE wallet_id = $1
			ORDER BY created_at DESC, id DESC
			LIMIT $2
		`
		rows, err = tx.Query(ctx, query, walletID, limit)
	}

	if err != nil {
		return nil, fmt.Errorf("ledger repo: get by wallet id: %w", err)
	}
	defer rows.Close()

	var entries []*ledger.WalletLedgerEntry
	for rows.Next() {
		var (
			id             uuid.UUID
			wID            uuid.UUID
			txnID          uuid.UUID
			dirStr         string
			amount         int64
			currencyStr    string
			balanceBefore  int64
			balanceAfter   int64
			createdAt      time.Time
		)

		err := rows.Scan(
			&id, &wID, &txnID, &dirStr,
			&amount, &currencyStr, &balanceBefore, &balanceAfter, &createdAt,
		)
		if err != nil {
			return nil, fmt.Errorf("ledger repo: scan: %w", err)
		}

		curr := money.Currency(currencyStr)
		m := money.NewFromInt64(amount, curr)
		bb := money.NewFromInt64(balanceBefore, curr)
		ba := money.NewFromInt64(balanceAfter, curr)

		entries = append(entries, ledger.Rehydrate(id, wID, txnID, ledger.Direction(dirStr), m, bb, ba, createdAt))
	}

	return entries, rows.Err()
}

func (r *LedgerRepository) CalculateBalance(ctx context.Context, tx port.DBTX, walletID uuid.UUID) (money.Money, int, error) {
	// Reconstruir o saldo via SQL acumulando créditos e débitos com integridade
	query := `
		SELECT
			COALESCE(SUM(CASE WHEN direction = 'CREDIT' THEN money_amount ELSE -money_amount END), 0) AS calculated_cents,
			COUNT(id) AS total_entries,
			COALESCE(MAX(money_currency)::text, '') AS currency
		FROM wallet_ledger_entries
		WHERE wallet_id = $1
	`
	var (
		calculatedCents int64
		totalEntries    int
		currencyStr     string
	)

	err := tx.QueryRow(ctx, query, walletID).Scan(&calculatedCents, &totalEntries, &currencyStr)
	if err != nil {
		return money.Money{}, 0, fmt.Errorf("ledger repo: calculate balance: %w", err)
	}

	// Se não houver lançamentos, busca a moeda da própria carteira
	if currencyStr == "" {
		walletQuery := `SELECT currency FROM wallets WHERE id = $1`
		err = tx.QueryRow(ctx, walletQuery, walletID).Scan(&currencyStr)
		if err != nil {
			return money.Money{}, 0, fmt.Errorf("ledger repo: get wallet currency for empty ledger: %w", err)
		}
	}

	curr := money.Currency(currencyStr)
	return money.NewFromInt64(calculatedCents, curr), totalEntries, nil
}
