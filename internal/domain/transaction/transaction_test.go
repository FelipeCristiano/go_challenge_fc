package transaction_test

import (
	"errors"
	"testing"
	"time"

	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/google/uuid"
)


func brl(s string) money.Money {
	m, err := money.NewFromExternalString(s, money.BRL)
	if err != nil {
		panic(err)
	}
	return m
}

func newExternalTxn(t *testing.T, kind transaction.Kind, amount string, ref *string) *transaction.WagerTransaction {
	t.Helper()
	m := brl(amount)
	txn, err := transaction.NewExternal(
		uuid.New(), uuid.New(), uuid.New(),
		"provider-a", "ext-txn-001", "provider-a:ext-txn-001", "hash-abc",
		"round-1", "game-1",
		kind, m, ref, nil,
	)
	if err != nil {
		t.Fatalf("NewExternal: %v", err)
	}
	return txn
}

func refPtr(s string) *string { return &s }


func TestNewExternal_BET(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindBet, "25.00", nil)
	if txn.Kind() != transaction.KindBet {
		t.Errorf("expected BET, got %s", txn.Kind())
	}
	if txn.Status() != transaction.StatusPending {
		t.Errorf("expected PENDING, got %s", txn.Status())
	}
	if txn.Money().Amount() != 2500 {
		t.Errorf("expected 2500 cents, got %d", txn.Money().Amount())
	}
}

func TestNewExternal_WIN(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindWin, "50.00", nil)
	if txn.Kind() != transaction.KindWin {
		t.Errorf("expected WIN")
	}
}

func TestNewExternal_LOSS_ZeroRequired(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindLoss, "0.00", nil)
	if txn.Kind() != transaction.KindLoss {
		t.Errorf("expected LOSS")
	}
	if !txn.Money().IsZero() {
		t.Error("LOSS must have zero money")
	}
}

func TestNewExternal_LOSS_NonZeroRejected(t *testing.T) {
	_, err := transaction.NewExternal(
		uuid.New(), uuid.New(), uuid.New(),
		"provider-a", "ext-001", "key-001", "hash",
		"round-1", "game-1",
		transaction.KindLoss, brl("1.00"), nil, nil,
	)
	if err == nil {
		t.Error("LOSS with non-zero amount must be rejected")
	}
}

func TestNewExternal_REFUND_RequiresReference(t *testing.T) {
	// Com referência: OK
	txn := newExternalTxn(t, transaction.KindRefund, "25.00", refPtr("original-txn"))
	if txn.Kind() != transaction.KindRefund {
		t.Errorf("expected REFUND")
	}

	// Sem referência: erro
	_, err := transaction.NewExternal(
		uuid.New(), uuid.New(), uuid.New(),
		"provider-a", "ext-001", "key-001", "hash",
		"round-1", "game-1",
		transaction.KindRefund, brl("25.00"), nil, nil,
	)
	if err == nil {
		t.Error("REFUND without reference must be rejected")
	}
}

func TestNewExternal_ROLLBACK_RequiresReference(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindRollback, "25.00", refPtr("original-txn"))
	if txn.Kind() != transaction.KindRollback {
		t.Errorf("expected ROLLBACK")
	}

	_, err := transaction.NewExternal(
		uuid.New(), uuid.New(), uuid.New(),
		"provider-a", "ext-001", "key-001", "hash",
		"round-1", "game-1",
		transaction.KindRollback, brl("25.00"), nil, nil,
	)
	if err == nil {
		t.Error("ROLLBACK without reference must be rejected")
	}
}

func TestNewExternal_ZeroAmount_NonLoss(t *testing.T) {
	for _, kind := range []transaction.Kind{
		transaction.KindBet, transaction.KindWin, transaction.KindRefund,
	} {
		kind := kind
		t.Run(string(kind), func(t *testing.T) {
			ref := refPtr("ref")
			_, err := transaction.NewExternal(
				uuid.New(), uuid.New(), uuid.New(),
				"provider-a", "ext-001", "key-001", "hash",
				"round-1", "game-1",
				kind, brl("0.00"), ref, nil,
			)
			if err == nil {
				t.Errorf("%s with zero amount must be rejected", kind)
			}
		})
	}
}

func TestNewExternal_OpeningForbidden(t *testing.T) {
	_, err := transaction.NewExternal(
		uuid.New(), uuid.New(), uuid.New(),
		"provider-a", "ext-001", "key-001", "hash",
		"round-1", "game-1",
		transaction.KindOpening, brl("100.00"), nil, nil,
	)
	if !errors.Is(err, errs.ErrOpeningForbidden) {
		t.Errorf("expected ErrOpeningForbidden, got: %v", err)
	}
}


func TestNewOpening_Success(t *testing.T) {
	txn, err := transaction.NewOpening(uuid.New(), uuid.New(), uuid.New(), brl("1000.00"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txn.Kind() != transaction.KindOpening {
		t.Errorf("expected OPENING, got %s", txn.Kind())
	}
	if txn.ProviderID() != nil {
		t.Error("OPENING should have nil providerID")
	}
	if txn.ExternalTransactionID() != nil {
		t.Error("OPENING should have nil externalTransactionID")
	}
	if txn.IdempotencyKey() != nil {
		t.Error("OPENING should have nil idempotencyKey")
	}
	if txn.ReferenceExternalTransactionID() != nil {
		t.Error("OPENING should have nil reference")
	}
}

func TestNewOpening_ZeroAmountRejected(t *testing.T) {
	_, err := transaction.NewOpening(uuid.New(), uuid.New(), uuid.New(), brl("0.00"))
	if err == nil {
		t.Error("OPENING with zero amount must be rejected")
	}
}


func TestMarkProcessed_FromPending(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindBet, "25.00", nil)
	resultBalance := brl("975.00")

	if err := txn.MarkProcessed(resultBalance); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txn.Status() != transaction.StatusProcessed {
		t.Errorf("expected PROCESSED, got %s", txn.Status())
	}
	if txn.ResultBalance() == nil || !txn.ResultBalance().Equal(resultBalance) {
		t.Error("resultBalance not set correctly")
	}
}

func TestMarkPendingReference_FromPending(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindRefund, "25.00", refPtr("ref"))

	if err := txn.MarkPendingReference(time.Now().Add(5 * time.Second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txn.Status() != transaction.StatusPendingReference {
		t.Errorf("expected PENDING_REFERENCE, got %s", txn.Status())
	}
	if txn.RetryCount() != 1 {
		t.Errorf("retryCount should be 1, got %d", txn.RetryCount())
	}
}

func TestMarkProcessed_FromPendingReference(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindRefund, "25.00", refPtr("ref"))
	_ = txn.MarkPendingReference(time.Now().Add(5 * time.Second))

	if err := txn.MarkProcessed(brl("1000.00")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txn.Status() != transaction.StatusProcessed {
		t.Errorf("expected PROCESSED, got %s", txn.Status())
	}
}

func TestMarkRejected(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindBet, "25.00", nil)
	if err := txn.MarkRejected(errs.CodeInsufficientFunds); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txn.Status() != transaction.StatusRejected {
		t.Errorf("expected REJECTED, got %s", txn.Status())
	}
	if txn.FailureCode() == nil || *txn.FailureCode() != errs.CodeInsufficientFunds {
		t.Error("failureCode not set correctly")
	}
}

func TestMarkFailed(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindBet, "25.00", nil)
	if err := txn.MarkFailed("INFRA_ERROR"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txn.Status() != transaction.StatusFailed {
		t.Errorf("expected FAILED, got %s", txn.Status())
	}
}


func TestTerminalStates_NoTransition(t *testing.T) {
	terminals := []transaction.Status{
		transaction.StatusProcessed,
		transaction.StatusRejected,
		transaction.StatusFailed,
	}
	for _, status := range terminals {
		status := status
		t.Run(string(status), func(t *testing.T) {
			txn := newExternalTxn(t, transaction.KindBet, "25.00", nil)
			// Forçar estado terminal via Rehydrate
			txn = transaction.Rehydrate(
				txn.ID(), txn.WalletID(), txn.PlayerID(),
				txn.ProviderID(), txn.ExternalTransactionID(), txn.IdempotencyKey(), txn.PayloadHash(),
				txn.RoundID(), txn.GameID(),
				txn.Kind(), status, txn.Money(),
				nil, nil, nil, nil, 0, nil, nil,
				time.Now(), time.Now(),
			)

			if err := txn.MarkProcessed(brl("100.00")); !errors.Is(err, errs.ErrTransactionTerminal) {
				t.Errorf("MarkProcessed on %s: expected ErrTransactionTerminal, got %v", status, err)
			}
			if err := txn.MarkRejected("code"); !errors.Is(err, errs.ErrTransactionTerminal) {
				t.Errorf("MarkRejected on %s: expected ErrTransactionTerminal, got %v", status, err)
			}
			if err := txn.MarkFailed("code"); !errors.Is(err, errs.ErrTransactionTerminal) {
				t.Errorf("MarkFailed on %s: expected ErrTransactionTerminal, got %v", status, err)
			}
		})
	}
}


func TestMarkPendingReference_OnlyFromPending(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindRefund, "25.00", refPtr("ref"))
	_ = txn.MarkPendingReference(time.Now().Add(5 * time.Second))

	err := txn.MarkPendingReference(time.Now().Add(10 * time.Second))
	if err == nil {
		t.Error("MarkPendingReference from PENDING_REFERENCE should fail")
	}
	if errors.Is(err, errs.ErrTransactionTerminal) {
		t.Error("error should not be ErrTransactionTerminal for non-terminal source")
	}
}

func TestIncrementRetry(t *testing.T) {
	txn := newExternalTxn(t, transaction.KindRefund, "25.00", refPtr("ref"))
	_ = txn.MarkPendingReference(time.Now().Add(5 * time.Second))

	if err := txn.IncrementRetry(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if txn.RetryCount() != 2 {
		t.Errorf("retryCount should be 2, got %d", txn.RetryCount())
	}
}


func TestDirectionFor(t *testing.T) {
	betKind := transaction.KindBet
	winKind := transaction.KindWin
	refundKind := transaction.KindRefund

	cases := []struct {
		kind     transaction.Kind
		refKind  *transaction.Kind
		wantDir  string
		wantErr  bool
	}{
		{transaction.KindBet, nil, "DEBIT", false},
		{transaction.KindWin, nil, "CREDIT", false},
		{transaction.KindRefund, nil, "CREDIT", false},
		{transaction.KindLoss, nil, "", true},
		{transaction.KindRollback, &betKind, "CREDIT", false},    // desfaz débito de BET → crédito
		{transaction.KindRollback, &winKind, "DEBIT", false},     // desfaz crédito de WIN → débito
		{transaction.KindRollback, &refundKind, "DEBIT", false},  // desfaz crédito de REFUND → débito
		{transaction.KindRollback, nil, "", true},                 // sem referência → erro
		{transaction.KindOpening, nil, "CREDIT", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.kind), func(t *testing.T) {
			dir, err := transaction.DirectionFor(tc.kind, tc.refKind)
			if tc.wantErr {
				if err == nil {
					t.Errorf("expected error for %s", tc.kind)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if dir != tc.wantDir {
				t.Errorf("expected direction %s, got %s", tc.wantDir, dir)
			}
		})
	}
}
