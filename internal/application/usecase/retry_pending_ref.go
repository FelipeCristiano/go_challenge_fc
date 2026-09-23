package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/events"
	"github.com/felipecristiano/desafio/internal/domain/ledger"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/outbox"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/infra/observability"
)

type RetryPendingReferencesUseCase struct {
	uow        port.UnitOfWork
	walletRepo port.WalletRepository
	txnRepo    port.WagerTransactionRepository
	ledgerRepo port.LedgerRepository
	outboxRepo port.OutboxRepository
	maxRetries int
}

func NewRetryPendingReferencesUseCase(
	uow port.UnitOfWork,
	walletRepo port.WalletRepository,
	txnRepo port.WagerTransactionRepository,
	ledgerRepo port.LedgerRepository,
	outboxRepo port.OutboxRepository,
	maxRetries int,
) *RetryPendingReferencesUseCase {
	if maxRetries <= 0 {
		maxRetries = 10
	}
	return &RetryPendingReferencesUseCase{
		uow:        uow,
		walletRepo: walletRepo,
		txnRepo:    txnRepo,
		ledgerRepo: ledgerRepo,
		outboxRepo: outboxRepo,
		maxRetries: maxRetries,
	}
}

// ExecuteBatch busca um lote de transações PENDING_REFERENCE e tenta resolvê-las.
func (uc *RetryPendingReferencesUseCase) ExecuteBatch(ctx context.Context, batchSize int) (int, error) {
	var pendingList []*transaction.WagerTransaction

	err := uc.uow.WithTx(ctx, func(tx port.DBTX) error {
		var err error
		pendingList, err = uc.txnRepo.GetPendingReferences(ctx, tx, batchSize)
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("retry pending references: get batch: %w", err)
	}

	resolvedCount := 0
	for _, txn := range pendingList {
		if err := uc.processSinglePending(ctx, txn); err == nil {
			resolvedCount++
		}
	}
	return resolvedCount, nil
}

func (uc *RetryPendingReferencesUseCase) processSinglePending(ctx context.Context, txn *transaction.WagerTransaction) error {
	return uc.uow.WithTx(ctx, func(tx port.DBTX) error {
		// Se excedeu o máximo de retentativas, finaliza como REJECTED
		if txn.RetryCount() >= uc.maxRetries {
			observability.WagerRetriesTotal.WithLabelValues("pending_ref", "max_reached").Inc()
			failCode := errs.CodeReferenceNotFound
			if err := txn.MarkRejected(failCode); err != nil {
				return err
			}
			if err := uc.txnRepo.Update(ctx, tx, txn); err != nil {
				return err
			}

			// Emite WagerTransactionRejected
			rejEvt := events.NewWagerTransactionRejected(
				txn.ID(), txn.WalletID(), txn.PlayerID(),
				txn.ProviderID(), txn.ExternalTransactionID(),
				string(txn.Kind()), failCode,
				*txn.CorrelationID(), nil,
			)
			outRej, err := outbox.New(
				rejEvt.EventID, rejEvt.EventType, txn.ID(), "WagerTransaction",
				txn.CorrelationID(), nil, rejEvt, time.Now().UTC(), 1,
			)
			if err != nil {
				return err
			}
			return uc.outboxRepo.Create(ctx, tx, outRej)
		}

		if txn.ReferenceExternalTransactionID() == nil || txn.ProviderID() == nil {
			return nil
		}

		// Busca a referência
		refTxn, err := uc.txnRepo.GetByProviderAndExternalID(ctx, tx, *txn.ProviderID(), *txn.ReferenceExternalTransactionID())
		if err != nil && !errors.Is(err, errs.ErrTransactionNotFound) {
			return err
		}

		// Se a referência ainda não existe, calcula próximo backoff exponencial
		if refTxn == nil {
			observability.WagerRetriesTotal.WithLabelValues("pending_ref", "retry").Inc()
			nextDelay := time.Duration(1<<txn.RetryCount()) * time.Second
			if nextDelay > 60*time.Second {
				nextDelay = 60 * time.Second
			}
			if err := txn.IncrementRetry(time.Now().UTC().Add(nextDelay)); err != nil {
				return err
			}
			return uc.txnRepo.Update(ctx, tx, txn)
		}

		// Referência encontrada: valida se está apta
		w, err := uc.walletRepo.GetByIDForUpdate(ctx, tx, txn.WalletID())
		if err != nil {
			return err
		}

		if refTxn.Status() != transaction.StatusProcessed {
			// Se a referência foi rejeitada ou falhou, rejeita a reversão
			if refTxn.Status().IsTerminal() {
				observability.WagerRetriesTotal.WithLabelValues("pending_ref", "rejected").Inc()
				failCode := errs.CodeReferenceNotProcessable
				if err := txn.MarkRejected(failCode); err != nil {
					return err
				}
				if err := uc.txnRepo.Update(ctx, tx, txn); err != nil {
					return err
				}
				rejEvt := events.NewWagerTransactionRejected(
					txn.ID(), txn.WalletID(), txn.PlayerID(),
					txn.ProviderID(), txn.ExternalTransactionID(),
					string(txn.Kind()), failCode,
					*txn.CorrelationID(), nil,
				)
				outRej, err := outbox.New(
					rejEvt.EventID, rejEvt.EventType, txn.ID(), "WagerTransaction",
					txn.CorrelationID(), nil, rejEvt, time.Now().UTC(), 1,
				)
				if err != nil {
					return err
				}
				return uc.outboxRepo.Create(ctx, tx, outRej)
			}
			// Se ainda está PENDING, continua aguardando
			return nil
		}

		txn.ResolveReference(refTxn.ID())

		// Aplica movimentação
		var (
			balanceBefore money.Money
			opErr         error
			dirStr        string
		)

		switch txn.Kind() {
		case transaction.KindRefund:
			balanceBefore, opErr = w.Credit(txn.Money())
			dirStr = string(ledger.DirectionCredit)

		case transaction.KindRollback:
			refKind := refTxn.Kind()
			dirStr, err = transaction.DirectionFor(transaction.KindRollback, &refKind)
			if err != nil {
				return err
			}
			if dirStr == string(ledger.DirectionDebit) {
				balanceBefore, opErr = w.DebitForRollback(txn.Money())
			} else {
				balanceBefore, opErr = w.Credit(txn.Money())
			}
		}

		if opErr != nil {
			var domErr *errs.DomainError
			failCode := errs.CodeInsufficientFunds
			if errors.As(opErr, &domErr) {
				failCode = domErr.Code
			}
			if err := txn.MarkRejected(failCode); err != nil {
				return err
			}
			if err := uc.txnRepo.Update(ctx, tx, txn); err != nil {
				return err
			}
			rejEvt := events.NewWagerTransactionRejected(
				txn.ID(), txn.WalletID(), txn.PlayerID(),
				txn.ProviderID(), txn.ExternalTransactionID(),
				string(txn.Kind()), failCode,
				*txn.CorrelationID(), nil,
			)
			outRej, err := outbox.New(
				rejEvt.EventID, rejEvt.EventType, txn.ID(), "WagerTransaction",
				txn.CorrelationID(), nil, rejEvt, time.Now().UTC(), 1,
			)
			if err != nil {
				return err
			}
			return uc.outboxRepo.Create(ctx, tx, outRej)
		}

		// Sucesso: marca como PROCESSED e comita
		observability.WagerRetriesTotal.WithLabelValues("pending_ref", "success").Inc()
		if err := txn.MarkProcessed(w.Balance()); err != nil {
			return err
		}
		if err := uc.walletRepo.Update(ctx, tx, w); err != nil {
			return err
		}
		if err := uc.txnRepo.Update(ctx, tx, txn); err != nil {
			return err
		}

		ledgerEntry, err := ledger.New(
			txn.WalletID(), txn.ID(), ledger.Direction(dirStr),
			txn.Money(), balanceBefore, w.Balance(),
		)
		if err != nil {
			return err
		}
		if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
			return err
		}

		procEvt := events.NewWagerTransactionProcessed(
			txn.ID(), txn.WalletID(), txn.PlayerID(),
			txn.ProviderID(), txn.ExternalTransactionID(),
			string(txn.Kind()), txn.Money(), w.Balance(),
			*txn.CorrelationID(), nil,
		)
		outProc, err := outbox.New(
			procEvt.EventID, procEvt.EventType, txn.ID(), "WagerTransaction",
			txn.CorrelationID(), nil, procEvt, time.Now().UTC(), 1,
		)
		if err != nil {
			return err
		}
		return uc.outboxRepo.Create(ctx, tx, outProc)
	})
}
