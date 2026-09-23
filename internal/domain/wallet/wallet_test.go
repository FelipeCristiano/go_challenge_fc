package wallet_test

import (
	"errors"
	"testing"
	"time"

	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/wallet"
	"github.com/google/uuid"
)

var (
	testID       = uuid.New()
	testPlayerID = uuid.New()
)

func TestNew_Success(t *testing.T) {
	bal, _ := money.NewFromExternalString("1000.00", money.BRL)
	w, err := wallet.New(testID, testPlayerID, money.BRL, bal)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if w.ID() != testID {
		t.Errorf("id mismatch")
	}
	if w.Version() != 1 {
		t.Errorf("initial version should be 1, got %d", w.Version())
	}
	if !w.Balance().Equal(bal) {
		t.Errorf("balance mismatch: got %s", w.Balance())
	}
}

func TestNew_ZeroBalance(t *testing.T) {
	w, err := wallet.New(testID, testPlayerID, money.BRL, money.Zero(money.BRL))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !w.Balance().IsZero() {
		t.Error("expected zero balance")
	}
	if w.Version() != 1 {
		t.Errorf("version should be 1, got %d", w.Version())
	}
}

func TestNew_NilID(t *testing.T) {
	_, err := wallet.New(uuid.Nil, testPlayerID, money.BRL, money.Zero(money.BRL))
	if err == nil {
		t.Error("expected error for nil wallet ID")
	}
}

func TestNew_NilPlayerID(t *testing.T) {
	_, err := wallet.New(testID, uuid.Nil, money.BRL, money.Zero(money.BRL))
	if err == nil {
		t.Error("expected error for nil player ID")
	}
}

func TestNew_CurrencyMismatch(t *testing.T) {
	bal := money.NewFromInt64(1000, money.USD)
	_, err := wallet.New(testID, testPlayerID, money.BRL, bal)
	if !errors.Is(err, errs.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got: %v", err)
	}
}

func TestNew_NegativeBalance(t *testing.T) {
	bal := money.NewFromInt64(-100, money.BRL)
	_, err := wallet.New(testID, testPlayerID, money.BRL, bal)
	if !errors.Is(err, errs.ErrNegativeBalance) {
		t.Errorf("expected ErrNegativeBalance, got: %v", err)
	}
}

func TestRehydrate_DoesNotModifyState(t *testing.T) {
	bal := money.NewFromInt64(50000, money.BRL) // 500.00
	w := wallet.Rehydrate(testID, testPlayerID, money.BRL, bal, 5, fixedTime(), fixedTime())
	if w.Version() != 5 {
		t.Errorf("version should be 5, got %d", w.Version())
	}
	if !w.Balance().Equal(bal) {
		t.Errorf("balance mismatch after rehydration")
	}
}

func TestDebit_Success(t *testing.T) {
	w := newWallet(t, "1000.00")
	betAmt, _ := money.NewFromExternalString("250.00", money.BRL)

	before, err := w.Debit(betAmt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !before.Equal(money.NewFromInt64(100000, money.BRL)) {
		t.Errorf("balanceBefore should be 1000.00, got %s", before)
	}
	if !w.Balance().Equal(money.NewFromInt64(75000, money.BRL)) {
		t.Errorf("balance should be 750.00 after debit, got %s", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("version should increment to 2, got %d", w.Version())
	}
}

func TestDebit_InsufficientFunds(t *testing.T) {
	w := newWallet(t, "100.00")
	amt, _ := money.NewFromExternalString("200.00", money.BRL)

	_, err := w.Debit(amt)
	if !errors.Is(err, errs.ErrInsufficientFunds) {
		t.Errorf("expected ErrInsufficientFunds, got: %v", err)
	}
	// Saldo não deve mudar
	if !w.Balance().Equal(money.NewFromInt64(10000, money.BRL)) {
		t.Errorf("balance should not change on failed debit")
	}
	// Versão não deve mudar
	if w.Version() != 1 {
		t.Errorf("version should not change on failed debit, got %d", w.Version())
	}
}

func TestDebit_ExactBalance(t *testing.T) {
	// Aposta exatamente igual ao saldo → saldo final zero
	w := newWallet(t, "100.00")
	amt, _ := money.NewFromExternalString("100.00", money.BRL)

	_, err := w.Debit(amt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !w.Balance().IsZero() {
		t.Errorf("balance should be zero after exact debit, got %s", w.Balance())
	}
}

func TestDebit_CurrencyMismatch(t *testing.T) {
	w := newWallet(t, "1000.00")
	amt := money.NewFromInt64(100, money.USD)

	_, err := w.Debit(amt)
	if !errors.Is(err, errs.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got: %v", err)
	}
}

func TestDebit_ZeroAmount(t *testing.T) {
	w := newWallet(t, "1000.00")
	_, err := w.Debit(money.Zero(money.BRL))
	if !errors.Is(err, errs.ErrInvalidAmount) {
		t.Errorf("expected ErrInvalidAmount for zero debit, got: %v", err)
	}
}

func TestDebitForRollback_DistinctErrorCode(t *testing.T) {
	w := newWallet(t, "50.00")
	amt, _ := money.NewFromExternalString("100.00", money.BRL)

	_, err := w.DebitForRollback(amt)
	if !errors.Is(err, errs.ErrRollbackInsufficientFunds) {
		t.Errorf("expected ErrRollbackInsufficientFunds, got: %v", err)
	}
	// Código de erro diferente do BET insuficiente
	if errors.Is(err, errs.ErrInsufficientFunds) {
		t.Error("ROLLBACK insufficient funds should have a distinct code from BET")
	}
}

func TestCredit_Success(t *testing.T) {
	w := newWallet(t, "100.00")
	winAmt, _ := money.NewFromExternalString("50.00", money.BRL)

	before, err := w.Credit(winAmt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !before.Equal(money.NewFromInt64(10000, money.BRL)) {
		t.Errorf("balanceBefore should be 100.00, got %s", before)
	}
	if !w.Balance().Equal(money.NewFromInt64(15000, money.BRL)) {
		t.Errorf("balance should be 150.00 after credit, got %s", w.Balance())
	}
	if w.Version() != 2 {
		t.Errorf("version should increment to 2, got %d", w.Version())
	}
}

func TestCredit_CurrencyMismatch(t *testing.T) {
	w := newWallet(t, "1000.00")
	amt := money.NewFromInt64(100, money.USD)

	_, err := w.Credit(amt)
	if !errors.Is(err, errs.ErrCurrencyMismatch) {
		t.Errorf("expected ErrCurrencyMismatch, got: %v", err)
	}
}

func TestConcurrency_TwoDebitsOneWallet_Sequential(t *testing.T) {
	// Simula comportamento do lock pessimista: execução serial para a mesma carteira.
	w := newWallet(t, "100.00")
	bet, _ := money.NewFromExternalString("80.00", money.BRL)

	// Primeira aposta: deve processar
	_, err1 := w.Debit(bet)
	if err1 != nil {
		t.Fatalf("first bet should succeed: %v", err1)
	}
	if !w.Balance().Equal(money.NewFromInt64(2000, money.BRL)) {
		t.Errorf("balance should be 20.00 after first bet, got %s", w.Balance())
	}

	// Segunda aposta: deve falhar (saldo insuficiente)
	_, err2 := w.Debit(bet)
	if !errors.Is(err2, errs.ErrInsufficientFunds) {
		t.Errorf("second bet should be rejected with ErrInsufficientFunds, got: %v", err2)
	}

	// Saldo final: 20.00 BRL
	if !w.Balance().Equal(money.NewFromInt64(2000, money.BRL)) {
		t.Errorf("final balance should be 20.00, got %s", w.Balance())
	}

	// Versão: incrementou apenas uma vez (só um débito bem-sucedido)
	if w.Version() != 2 {
		t.Errorf("version should be 2, got %d", w.Version())
	}
}

func newWallet(t *testing.T, balanceStr string) *wallet.Wallet {
	t.Helper()
	bal, err := money.NewFromExternalString(balanceStr, money.BRL)
	if err != nil {
		t.Fatalf("invalid balance %q: %v", balanceStr, err)
	}
	w, err := wallet.New(uuid.New(), uuid.New(), money.BRL, bal)
	if err != nil {
		t.Fatalf("wallet.New: %v", err)
	}
	return w
}

func fixedTime() time.Time {
	return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
}
