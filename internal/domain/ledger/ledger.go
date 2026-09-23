package ledger

import (
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/google/uuid"
)

type Direction string

const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

type WalletLedgerEntry struct {
	id            uuid.UUID
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	money         money.Money
	balanceBefore money.Money
	balanceAfter  money.Money
	createdAt     time.Time
}

func New(
	walletID uuid.UUID,
	transactionID uuid.UUID,
	direction Direction,
	m money.Money,
	balanceBefore money.Money,
	balanceAfter money.Money,
) (*WalletLedgerEntry, error) {
	if !m.IsPositive() {
		return nil, fmt.Errorf("ledger: money amount must be positive, got %s", m)
	}
	if m.Currency() != balanceBefore.Currency() || m.Currency() != balanceAfter.Currency() {
		return nil, fmt.Errorf("ledger: currency mismatch between money (%s), balanceBefore (%s) and balanceAfter (%s)",
			m.Currency(), balanceBefore.Currency(), balanceAfter.Currency())
	}

	switch direction {
	case DirectionDebit:
		expected, err := balanceBefore.Sub(m)
		if err != nil {
			return nil, fmt.Errorf("ledger: debit arithmetic error: %w", err)
		}
		if !expected.Equal(balanceAfter) {
			return nil, fmt.Errorf("ledger: debit invariant violated: %s - %s = %s, but got %s",
				balanceBefore, m, expected, balanceAfter)
		}
	case DirectionCredit:
		expected, err := balanceBefore.Add(m)
		if err != nil {
			return nil, fmt.Errorf("ledger: credit arithmetic error: %w", err)
		}
		if !expected.Equal(balanceAfter) {
			return nil, fmt.Errorf("ledger: credit invariant violated: %s + %s = %s, but got %s",
				balanceBefore, m, expected, balanceAfter)
		}
	default:
		return nil, fmt.Errorf("ledger: invalid direction %q", direction)
	}

	return &WalletLedgerEntry{
		id:            uuid.New(),
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		money:         m,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     time.Now().UTC(),
	}, nil
}

func Rehydrate(
	id, walletID, transactionID uuid.UUID,
	direction Direction,
	m, balanceBefore, balanceAfter money.Money,
	createdAt time.Time,
) *WalletLedgerEntry {
	return &WalletLedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		money:         m,
		balanceBefore: balanceBefore,
		balanceAfter:  balanceAfter,
		createdAt:     createdAt,
	}
}


func (e *WalletLedgerEntry) ID() uuid.UUID            { return e.id }
func (e *WalletLedgerEntry) WalletID() uuid.UUID      { return e.walletID }
func (e *WalletLedgerEntry) TransactionID() uuid.UUID { return e.transactionID }
func (e *WalletLedgerEntry) Direction() Direction     { return e.direction }
func (e *WalletLedgerEntry) Money() money.Money       { return e.money }
func (e *WalletLedgerEntry) BalanceBefore() money.Money { return e.balanceBefore }
func (e *WalletLedgerEntry) BalanceAfter() money.Money  { return e.balanceAfter }
func (e *WalletLedgerEntry) CreatedAt() time.Time     { return e.createdAt }
