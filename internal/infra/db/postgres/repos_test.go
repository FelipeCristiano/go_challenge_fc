//go:build integration

package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/inbox"
	"github.com/felipecristiano/desafio/internal/domain/ledger"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/outbox"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/domain/wallet"
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

func TestPostgresRepositories_Integration(t *testing.T) {
	pool := getTestPool(t)
	defer pool.Close()

	ctx := context.Background()
	uow := postgres.NewUnitOfWork(pool)
	walletRepo := postgres.NewWalletRepository()
	txnRepo := postgres.NewWagerTransactionRepository()
	ledgerRepo := postgres.NewLedgerRepository()
	inboxRepo := postgres.NewInboxRepository()
	outboxRepo := postgres.NewOutboxRepository()

	playerID := uuid.New()
	walletID := uuid.New()
	initBal := money.NewFromInt64(100000, money.BRL) // 1000.00 BRL

	// 1. Teste de criação e leitura de Wallet
	w, err := wallet.New(walletID, playerID, money.BRL, initBal)
	if err != nil {
		t.Fatalf("failed to create wallet: %v", err)
	}

	err = uow.WithTx(ctx, func(tx port.DBTX) error {
		return walletRepo.Create(ctx, tx, w)
	})
	if err != nil {
		t.Fatalf("walletRepo.Create failed: %v", err)
	}

	// 2. Lock pessimista (GetByIDForUpdate)
	err = uow.WithTx(ctx, func(tx port.DBTX) error {
		fetched, err := walletRepo.GetByIDForUpdate(ctx, tx, walletID)
		if err != nil {
			return err
		}
		if fetched.Balance().Amount() != 100000 {
			t.Errorf("expected 100000 cents balance, got %d", fetched.Balance().Amount())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("GetByIDForUpdate failed: %v", err)
	}

	// 3. WagerTransaction, Ledger e Outbox em uma mesma transação atômica
	txnID := uuid.New()
	betAmt := money.NewFromInt64(2500, money.BRL) // 25.00 BRL
	refTxnID := "tx-123"

	txn, err := transaction.NewExternal(
		txnID, walletID, playerID,
		"provider-test", refTxnID, "provider-test:tx-123", "hash123",
		"round-1", "game-1", transaction.KindBet, betAmt, nil, nil,
	)
	if err != nil {
		t.Fatalf("transaction.NewExternal failed: %v", err)
	}

	err = uow.WithTx(ctx, func(tx port.DBTX) error {
		// Deduz saldo na carteira
		before, err := w.Debit(betAmt)
		if err != nil {
			return err
		}
		if err := walletRepo.Update(ctx, tx, w); err != nil {
			return err
		}

		if err := txn.MarkProcessed(w.Balance()); err != nil {
			return err
		}
		if err := txnRepo.Create(ctx, tx, txn); err != nil {
			return err
		}

		// Cria lançamento no ledger
		entry, err := ledger.New(walletID, txnID, ledger.DirectionDebit, betAmt, before, w.Balance())
		if err != nil {
			return err
		}
		if err := ledgerRepo.Create(ctx, tx, entry); err != nil {
			return err
		}

		// Cria outbox event
		outEvt, err := outbox.New(uuid.New(), "WagerTransactionProcessed", txnID, "WagerTransaction", nil, nil, map[string]string{"status": "OK"}, time.Now().UTC(), 1)
		if err != nil {
			return err
		}
		return outboxRepo.Create(ctx, tx, outEvt)
	})
	if err != nil {
		t.Fatalf("atomic transaction failed: %v", err)
	}

	// 4. Teste de Inbox: deduplicação
	inboxMsg := inbox.New("consumer-test", "msg-001", "hash-msg")
	err = uow.WithTx(ctx, func(tx port.DBTX) error {
		inserted, err := inboxRepo.CreateIfAbsent(ctx, tx, inboxMsg)
		if err != nil {
			return err
		}
		if !inserted {
			t.Errorf("expected msg-001 to be inserted")
		}
		// Segunda inserção com mesmo message_id deve retornar false
		inserted2, err := inboxRepo.CreateIfAbsent(ctx, tx, inboxMsg)
		if err != nil {
			return err
		}
		if inserted2 {
			t.Errorf("expected duplicate msg-001 to be ignored")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inbox test failed: %v", err)
	}

	// 5. Teste de Outbox: FetchPendingForPublish & MarkPublished
	err = uow.WithTx(ctx, func(tx port.DBTX) error {
		events, err := outboxRepo.FetchPendingForPublish(ctx, tx, 10)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			t.Errorf("expected at least 1 pending event")
		}
		return outboxRepo.MarkPublished(ctx, tx, events[0].ID())
	})
	if err != nil {
		t.Fatalf("outbox publish test failed: %v", err)
	}

	// 6. Teste de Reconciliação no Ledger
	err = uow.WithTx(ctx, func(tx port.DBTX) error {
		calcBal, totalEntries, err := ledgerRepo.CalculateBalance(ctx, tx, walletID)
		if err != nil {
			return err
		}
		if totalEntries != 1 {
			t.Errorf("expected 1 entry in ledger, got %d", totalEntries)
		}
		// Apenas 1 débito de 25.00: soma créditos - débitos = -25.00
		if calcBal.Amount() != -2500 {
			t.Errorf("expected calculated ledger delta of -2500, got %d", calcBal.Amount())
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ledger calculate balance test failed: %v", err)
	}
}
