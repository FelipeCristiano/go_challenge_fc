//go:build integration

package usecase_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/felipecristiano/desafio/internal/application/usecase"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/infra/db/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func getTestPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://desafio:desafio@localhost:5432/desafio?sslmode=disable"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pool, err := postgres.NewPool(ctx, dbURL)
	if err != nil {
		t.Fatalf("failed to connect to test db: %v", err)
	}
	return pool
}

func setupUseCases(pool *pgxpool.Pool) (
	*usecase.OpenWalletUseCase,
	*usecase.ProcessWagerUseCase,
	*usecase.ReconcileWalletUseCase,
	*usecase.RetryPendingReferencesUseCase,
) {
	uow := postgres.NewUnitOfWork(pool)
	walletRepo := postgres.NewWalletRepository()
	txnRepo := postgres.NewWagerTransactionRepository()
	ledgerRepo := postgres.NewLedgerRepository()
	inboxRepo := postgres.NewInboxRepository()
	outboxRepo := postgres.NewOutboxRepository()

	return usecase.NewOpenWalletUseCase(uow, walletRepo, txnRepo, ledgerRepo, outboxRepo),
		usecase.NewProcessWagerUseCase(uow, walletRepo, txnRepo, ledgerRepo, inboxRepo, outboxRepo),
		usecase.NewReconcileWalletUseCase(uow, walletRepo, ledgerRepo),
		usecase.NewRetryPendingReferencesUseCase(uow, walletRepo, txnRepo, ledgerRepo, outboxRepo, 5)
}

func TestUseCases_EndToEndFinancialFlow(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	// Limpeza antes do teste para garantir ambiente prístino
	_, _ = pool.Exec(ctx, "TRUNCATE outbox_events, inbox_messages, wallet_ledger_entries, wager_transactions, wallets CASCADE")

	openWalletUC, processWagerUC, reconcileUC, retryPendingUC := setupUseCases(pool)

	playerID := uuid.New()
	correlationID := uuid.New()

	// 1. Abertura de Carteira com 100.00 BRL
	initBal := money.NewFromInt64(10000, money.BRL) // 100.00 BRL
	walletOut, err := openWalletUC.Execute(ctx, usecase.OpenWalletInput{
		PlayerID:       playerID,
		InitialBalance: initBal,
		CorrelationID:  correlationID,
	})
	if err != nil {
		t.Fatalf("OpenWallet failed: %v", err)
	}
	if walletOut.Balance.Amount() != 10000 {
		t.Fatalf("expected balance 10000, got %d", walletOut.Balance.Amount())
	}
	walletID := walletOut.ID

	// 2. Reconciliação imediata: deve ser consistente (1 crédito no ledger da abertura)
	recOut, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: walletID})
	if err != nil {
		t.Fatalf("ReconcileWallet failed: %v", err)
	}
	if !recOut.Consistent || recOut.CheckedEntries != 1 {
		t.Fatalf("reconcile inconsistency: consistent=%v, entries=%d", recOut.Consistent, recOut.CheckedEntries)
	}

	// 3. Cenário Obrigatório de Concorrência: 2 apostas de 80.00 sobre saldo de 100.00
	betAmount := money.NewFromInt64(8000, money.BRL) // 80.00 BRL

	testSuffix := uuid.New().String()[:8]
	bet1ExtID := "bet-001-" + testSuffix
	bet1IdemKey := "provider-a:" + bet1ExtID

	bet2ExtID := "bet-002-" + testSuffix
	bet2IdemKey := "provider-a:" + bet2ExtID

	// Primeira aposta de 80.00
	bet1Out, err := processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "HTTP",
		IdempotencyKey:        bet1IdemKey,
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: bet1ExtID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-test",
		Kind:                  transaction.KindBet,
		Money:                 betAmount,
	})
	if err != nil {
		t.Fatalf("first bet failed: %v", err)
	}
	if bet1Out.Status != transaction.StatusProcessed || bet1Out.Balance.Amount() != 2000 {
		t.Fatalf("first bet unexpected: status=%s, balance=%s", bet1Out.Status, bet1Out.Balance)
	}

	// Segunda aposta de 80.00 (deve ser rejeitada por saldo insuficiente, saldo remanescente 20.00)
	bet2Out, err := processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "HTTP",
		IdempotencyKey:        bet2IdemKey,
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: bet2ExtID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-test",
		Kind:                  transaction.KindBet,
		Money:                 betAmount,
	})
	if err != nil {
		t.Fatalf("second bet failed: %v", err)
	}
	if bet2Out.Status != transaction.StatusRejected {
		t.Fatalf("second bet expected REJECTED, got %s", bet2Out.Status)
	}
	if bet2Out.Balance.Amount() != 2000 {
		t.Fatalf("balance after second bet must remain 2000 (20.00 BRL), got %d", bet2Out.Balance.Amount())
	}

	// 4. Teste de Replay Idempotente da primeira aposta
	replayOut, err := processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "HTTP",
		IdempotencyKey:        bet1IdemKey,
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: bet1ExtID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-test",
		Kind:                  transaction.KindBet,
		Money:                 betAmount,
	})
	if err != nil {
		t.Fatalf("replay bet failed: %v", err)
	}
	if !replayOut.IdempotentReplay {
		t.Fatalf("expected IdempotentReplay=true")
	}
	if replayOut.Balance.Amount() != 2000 {
		t.Fatalf("expected historic balance 20.00, got %s", replayOut.Balance)
	}

	// 5. Teste de Conflito de Chave com Payload Distinto
	_, err = processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "HTTP",
		IdempotencyKey:        bet1IdemKey,
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: bet1ExtID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-1",
		GameID:                "fortune-test",
		Kind:                  transaction.KindBet,
		Money:                 money.NewFromInt64(5000, money.BRL), // Payload com valor diferente!
	})
	if !errors.Is(err, errs.ErrPayloadConflict) {
		t.Fatalf("expected ErrPayloadConflict, got: %v", err)
	}

	// 6. Teste de Reversão REFUND antecipada (PENDING_REFERENCE)
	refundRefID := "bet-future-" + testSuffix
	refundOut, err := processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                         "HTTP",
		IdempotencyKey:                 "provider-a:ref-001-" + testSuffix,
		CorrelationID:                  uuid.New(),
		ProviderID:                     "provider-a",
		ExternalTransactionID:          "ref-001-" + testSuffix,
		PlayerID:                       playerID,
		WalletID:                       walletID,
		RoundID:                        "round-future",
		GameID:                         "fortune-test",
		Kind:                           transaction.KindRefund,
		Money:                          money.NewFromInt64(1000, money.BRL),
		ReferenceExternalTransactionID: &refundRefID,
	})
	if err != nil {
		t.Fatalf("refund pending failed: %v", err)
	}
	if refundOut.Status != transaction.StatusPendingReference {
		t.Fatalf("expected StatusPendingReference, got %s", refundOut.Status)
	}

	// Agora processamos a BET que estava faltando
	_, err = processWagerUC.Execute(ctx, usecase.ProcessWagerInput{
		Source:                "HTTP",
		IdempotencyKey:        "provider-a:bet-future-" + testSuffix,
		CorrelationID:         uuid.New(),
		ProviderID:            "provider-a",
		ExternalTransactionID: refundRefID,
		PlayerID:              playerID,
		WalletID:              walletID,
		RoundID:               "round-future",
		GameID:                "fortune-test",
		Kind:                  transaction.KindBet,
		Money:                 money.NewFromInt64(1000, money.BRL),
	})
	if err != nil {
		t.Fatalf("future bet processing failed: %v", err)
	}

	// Executa worker de resolução de referências pendentes
	resolved, err := retryPendingUC.ExecuteBatch(ctx, 10)
	if err != nil {
		t.Fatalf("retry pending references failed: %v", err)
	}
	if resolved != 1 {
		t.Fatalf("expected 1 resolved reference, got %d", resolved)
	}

	// 7. Reconciliação Final de Todas as Movimentações
	finalRec, err := reconcileUC.Execute(ctx, usecase.ReconcileWalletInput{WalletID: walletID})
	if err != nil {
		t.Fatalf("final reconcile failed: %v", err)
	}
	if !finalRec.Consistent {
		t.Fatalf("ledger is inconsistent! stored=%s, calculated=%s, diff=%s",
			finalRec.StoredBalance, finalRec.CalculatedBalance, finalRec.Difference)
	}
}
