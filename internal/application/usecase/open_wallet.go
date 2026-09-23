package usecase

import (
	"context"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/events"
	"github.com/felipecristiano/desafio/internal/domain/ledger"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/outbox"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/domain/wallet"
	"github.com/google/uuid"
)

type OpenWalletInput struct {
	PlayerID       uuid.UUID
	InitialBalance money.Money
	CorrelationID  uuid.UUID
}

type OpenWalletOutput struct {
	ID       uuid.UUID
	PlayerID uuid.UUID
	Balance  money.Money
	Version  int64
}

type OpenWalletUseCase struct {
	uow        port.UnitOfWork
	walletRepo port.WalletRepository
	txnRepo    port.WagerTransactionRepository
	ledgerRepo port.LedgerRepository
	outboxRepo port.OutboxRepository
}

func NewOpenWalletUseCase(
	uow port.UnitOfWork,
	walletRepo port.WalletRepository,
	txnRepo port.WagerTransactionRepository,
	ledgerRepo port.LedgerRepository,
	outboxRepo port.OutboxRepository,
) *OpenWalletUseCase {
	return &OpenWalletUseCase{
		uow:        uow,
		walletRepo: walletRepo,
		txnRepo:    txnRepo,
		ledgerRepo: ledgerRepo,
		outboxRepo: outboxRepo,
	}
}

func (uc *OpenWalletUseCase) Execute(ctx context.Context, input OpenWalletInput) (*OpenWalletOutput, error) {
	walletID := uuid.New()
	w, err := wallet.New(walletID, input.PlayerID, input.InitialBalance.Currency(), input.InitialBalance)
	if err != nil {
		return nil, err
	}

	err = uc.uow.WithTx(ctx, func(tx port.DBTX) error {
		// 1. Persiste a carteira
		if err := uc.walletRepo.Create(ctx, tx, w); err != nil {
			return err
		}

		// 2. Se o saldo inicial for positivo (> 0), cria OPENING transaction, ledger entry e outbox events
		if input.InitialBalance.IsPositive() {
			txnID := uuid.New()
			openingTxn, err := transaction.NewOpening(txnID, walletID, input.PlayerID, input.InitialBalance)
			if err != nil {
				return err
			}
			if err := openingTxn.MarkProcessed(input.InitialBalance); err != nil {
				return err
			}
			if err := uc.txnRepo.Create(ctx, tx, openingTxn); err != nil {
				return err
			}

			// Lançamento de crédito no ledger (balanceBefore = 0, balanceAfter = initialBalance)
			zeroMoney := money.Zero(input.InitialBalance.Currency())
			ledgerEntry, err := ledger.New(
				walletID,
				txnID,
				ledger.DirectionCredit,
				input.InitialBalance,
				zeroMoney,
				input.InitialBalance,
			)
			if err != nil {
				return fmt.Errorf("open wallet: create ledger entry: %w", err)
			}
			if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
				return err
			}

			// Outbox: WagerTransactionProcessed
			evtProcessed := events.NewWagerTransactionProcessed(
				txnID, walletID, input.PlayerID,
				nil, nil,
				string(transaction.KindOpening),
				input.InitialBalance,
				input.InitialBalance,
				input.CorrelationID,
				nil,
			)
			outboxEvt1, err := outbox.New(
				evtProcessed.EventID,
				evtProcessed.EventType,
				walletID,
				"Wallet",
				&input.CorrelationID,
				nil,
				evtProcessed,
				time.Now().UTC(),
				1,
			)
			if err != nil {
				return err
			}
			if err := uc.outboxRepo.Create(ctx, tx, outboxEvt1); err != nil {
				return err
			}

			// Outbox: WalletBalanceChanged
			evtBalChanged := events.NewWalletBalanceChanged(
				walletID,
				txnID,
				string(ledger.DirectionCredit),
				input.InitialBalance,
				zeroMoney,
				input.InitialBalance,
				w.Version(),
				input.CorrelationID,
				nil,
			)
			outboxEvt2, err := outbox.New(
				evtBalChanged.EventID,
				evtBalChanged.EventType,
				walletID,
				"Wallet",
				&input.CorrelationID,
				nil,
				evtBalChanged,
				time.Now().UTC(),
				1,
			)
			if err != nil {
				return err
			}
			if err := uc.outboxRepo.Create(ctx, tx, outboxEvt2); err != nil {
				return err
			}
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	return &OpenWalletOutput{
		ID:       w.ID(),
		PlayerID: w.PlayerID(),
		Balance:  w.Balance(),
		Version:  w.Version(),
	}, nil
}
