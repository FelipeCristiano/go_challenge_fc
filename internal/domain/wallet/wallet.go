package wallet

import (
	"time"

	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/google/uuid"
)

type Wallet struct {
	id        uuid.UUID
	playerID  uuid.UUID
	currency  money.Currency
	balance   money.Money
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func New(id, playerID uuid.UUID, currency money.Currency, initialBalance money.Money) (*Wallet, error) {
	if id == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "wallet ID is required")
	}
	if playerID == uuid.Nil {
		return nil, errs.Newf(errs.CodeInvalidInput, "player ID is required")
	}
	if err := validateCurrency(currency); err != nil {
		return nil, err
	}
	if initialBalance.Currency() != currency {
		return nil, errs.ErrCurrencyMismatch
	}
	if initialBalance.IsNegative() {
		return nil, errs.ErrNegativeBalance
	}

	now := time.Now().UTC()
	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  currency,
		balance:   initialBalance,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}, nil
}

func Rehydrate(
	id, playerID uuid.UUID,
	currency money.Currency,
	balance money.Money,
	version int64,
	createdAt, updatedAt time.Time,
) *Wallet {
	return &Wallet{
		id:        id,
		playerID:  playerID,
		currency:  currency,
		balance:   balance,
		version:   version,
		createdAt: createdAt,
		updatedAt: updatedAt,
	}
}


func (w *Wallet) Debit(m money.Money) (balanceBefore money.Money, err error) {
	if err := w.validateOperation(m); err != nil {
		return money.Money{}, err
	}
	balanceBefore = w.balance
	newBalance, err := w.balance.Sub(m)
	if err != nil {
		return money.Money{}, err
	}
	if newBalance.IsNegative() {
		return money.Money{}, errs.ErrInsufficientFunds
	}
	w.balance = newBalance
	w.version++
	w.updatedAt = time.Now().UTC()
	return balanceBefore, nil
}

func (w *Wallet) DebitForRollback(m money.Money) (balanceBefore money.Money, err error) {
	if err := w.validateOperation(m); err != nil {
		return money.Money{}, err
	}
	balanceBefore = w.balance
	newBalance, err := w.balance.Sub(m)
	if err != nil {
		return money.Money{}, err
	}
	if newBalance.IsNegative() {
		return money.Money{}, errs.ErrRollbackInsufficientFunds
	}
	w.balance = newBalance
	w.version++
	w.updatedAt = time.Now().UTC()
	return balanceBefore, nil
}

func (w *Wallet) Credit(m money.Money) (balanceBefore money.Money, err error) {
	if err := w.validateOperation(m); err != nil {
		return money.Money{}, err
	}
	balanceBefore = w.balance
	newBalance, err := w.balance.Add(m)
	if err != nil {
		return money.Money{}, err
	}
	w.balance = newBalance
	w.version++
	w.updatedAt = time.Now().UTC()
	return balanceBefore, nil
}


func (w *Wallet) ID() uuid.UUID          { return w.id }
func (w *Wallet) PlayerID() uuid.UUID    { return w.playerID }
func (w *Wallet) Currency() money.Currency { return w.currency }
func (w *Wallet) Balance() money.Money   { return w.balance }
func (w *Wallet) Version() int64         { return w.version }
func (w *Wallet) CreatedAt() time.Time   { return w.createdAt }
func (w *Wallet) UpdatedAt() time.Time   { return w.updatedAt }


func (w *Wallet) validateOperation(m money.Money) error {
	if m.Currency() != w.currency {
		return errs.ErrCurrencyMismatch
	}
	if !m.IsPositive() {
		return errs.ErrInvalidAmount
	}
	return nil
}

func validateCurrency(c money.Currency) error {
	switch c {
	case money.BRL, money.USD, money.EUR:
		return nil
	}
	return errs.Newf(errs.CodeInvalidInput, "unsupported currency: %q", c)
}
