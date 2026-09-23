package ledger_test

import (
	"testing"
	"time"

	"github.com/felipecristiano/desafio/internal/domain/ledger"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/google/uuid"
)

func brl(amount int64) money.Money {
	return money.NewFromInt64(amount, money.BRL)
}

func TestNew_Credit_Success(t *testing.T) {
	entry, err := ledger.New(
		uuid.New(), uuid.New(),
		ledger.DirectionCredit,
		brl(5000),  // 50.00
		brl(10000), // 100.00
		brl(15000), // 150.00
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Direction() != ledger.DirectionCredit {
		t.Errorf("expected CREDIT, got %s", entry.Direction())
	}
	if entry.Money().Amount() != 5000 {
		t.Errorf("money amount mismatch")
	}
	if entry.BalanceBefore().Amount() != 10000 {
		t.Errorf("balanceBefore mismatch")
	}
	if entry.BalanceAfter().Amount() != 15000 {
		t.Errorf("balanceAfter mismatch")
	}
	if entry.ID() == uuid.Nil {
		t.Error("entry ID should be set")
	}
}

func TestNew_Debit_Success(t *testing.T) {
	entry, err := ledger.New(
		uuid.New(), uuid.New(),
		ledger.DirectionDebit,
		brl(2500),  // 25.00
		brl(10000), // 100.00
		brl(7500),  // 75.00
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if entry.Direction() != ledger.DirectionDebit {
		t.Errorf("expected DEBIT, got %s", entry.Direction())
	}
}

func TestNew_Credit_WrongArithmetic(t *testing.T) {
	// 100.00 + 50.00 ≠ 200.00
	_, err := ledger.New(
		uuid.New(), uuid.New(),
		ledger.DirectionCredit,
		brl(5000),  // 50.00
		brl(10000), // 100.00
		brl(20000), // 200.00 (ERRADO)
	)
	if err == nil {
		t.Error("expected error for wrong arithmetic in credit")
	}
}

func TestNew_Debit_WrongArithmetic(t *testing.T) {
	// 100.00 - 25.00 ≠ 50.00
	_, err := ledger.New(
		uuid.New(), uuid.New(),
		ledger.DirectionDebit,
		brl(2500),  // 25.00
		brl(10000), // 100.00
		brl(5000),  // 50.00 (ERRADO)
	)
	if err == nil {
		t.Error("expected error for wrong arithmetic in debit")
	}
}

func TestNew_ZeroMoney(t *testing.T) {
	_, err := ledger.New(
		uuid.New(), uuid.New(),
		ledger.DirectionCredit,
		brl(0), // ZERO — inválido
		brl(10000),
		brl(10000),
	)
	if err == nil {
		t.Error("expected error for zero money amount")
	}
}

func TestNew_NegativeMoney(t *testing.T) {
	_, err := ledger.New(
		uuid.New(), uuid.New(),
		ledger.DirectionCredit,
		brl(-100), // negativo — inválido
		brl(10000),
		brl(9900),
	)
	if err == nil {
		t.Error("expected error for negative money amount")
	}
}

func TestNew_InvalidDirection(t *testing.T) {
	_, err := ledger.New(
		uuid.New(), uuid.New(),
		ledger.Direction("INVALID"),
		brl(5000),
		brl(10000),
		brl(15000),
	)
	if err == nil {
		t.Error("expected error for invalid direction")
	}
}

func TestRehydrate(t *testing.T) {
	id, wID, tID := uuid.New(), uuid.New(), uuid.New()
	entry := ledger.Rehydrate(
		id, wID, tID,
		ledger.DirectionDebit,
		brl(2500), brl(10000), brl(7500),
		fixedTime(),
	)
	if entry.ID() != id {
		t.Error("id mismatch after rehydration")
	}
	if entry.Direction() != ledger.DirectionDebit {
		t.Error("direction mismatch after rehydration")
	}
}

func fixedTime() time.Time {
	return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
}
