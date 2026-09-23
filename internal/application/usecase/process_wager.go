package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/errs"
	"github.com/felipecristiano/desafio/internal/domain/events"
	"github.com/felipecristiano/desafio/internal/domain/inbox"
	"github.com/felipecristiano/desafio/internal/domain/ledger"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/felipecristiano/desafio/internal/domain/outbox"
	"github.com/felipecristiano/desafio/internal/domain/transaction"
	"github.com/felipecristiano/desafio/internal/domain/wallet"
	"github.com/google/uuid"
)

type ProcessWagerInput struct {
	// Metadados de transporte / consumidor
	Source         string // "HTTP" ou "SQS"
	ConsumerName   string // Usado se Source == "SQS"
	MessageID      string // Usado se Source == "SQS"
	IdempotencyKey string
	CorrelationID  uuid.UUID

	// Dados da operação
	ProviderID                     string
	ExternalTransactionID          string
	PlayerID                       uuid.UUID
	WalletID                       uuid.UUID
	RoundID                        string
	GameID                         string
	Kind                           transaction.Kind
	Money                          money.Money
	ReferenceExternalTransactionID *string
}

type ProcessWagerOutput struct {
	TransactionID   uuid.UUID
	Status          transaction.Status
	Balance         money.Money
	IdempotentReplay bool
	FailureCode     *string
}

type ProcessWagerUseCase struct {
	uow        port.UnitOfWork
	walletRepo port.WalletRepository
	txnRepo    port.WagerTransactionRepository
	ledgerRepo port.LedgerRepository
	inboxRepo  port.InboxRepository
	outboxRepo port.OutboxRepository
}

func NewProcessWagerUseCase(
	uow port.UnitOfWork,
	walletRepo port.WalletRepository,
	txnRepo port.WagerTransactionRepository,
	ledgerRepo port.LedgerRepository,
	inboxRepo port.InboxRepository,
	outboxRepo port.OutboxRepository,
) *ProcessWagerUseCase {
	return &ProcessWagerUseCase{
		uow:        uow,
		walletRepo: walletRepo,
		txnRepo:    txnRepo,
		ledgerRepo: ledgerRepo,
		inboxRepo:  inboxRepo,
		outboxRepo: outboxRepo,
	}
}

func (uc *ProcessWagerUseCase) Execute(ctx context.Context, input ProcessWagerInput) (*ProcessWagerOutput, error) {
	if input.Kind == transaction.KindOpening {
		return nil, errs.ErrOpeningForbidden
	}

	// 1. Calcula hash determinístico dos campos de negócio canônicos
	payloadMap := map[string]any{
		"providerId":            input.ProviderID,
		"externalTransactionId": input.ExternalTransactionID,
		"playerId":              input.PlayerID.String(),
		"walletId":              input.WalletID.String(),
		"roundId":               input.RoundID,
		"gameId":                input.GameID,
		"kind":                  string(input.Kind),
		"money": map[string]any{
			"amount":   input.Money.String(),
			"currency": string(input.Money.Currency()),
		},
	}
	if input.ReferenceExternalTransactionID != nil && *input.ReferenceExternalTransactionID != "" {
		payloadMap["referenceExternalTransactionId"] = *input.ReferenceExternalTransactionID
	}

	currentHash, err := CanonicalPayloadHash(payloadMap)
	if err != nil {
		return nil, fmt.Errorf("process wager: calculate payload hash: %w", err)
	}

	var output *ProcessWagerOutput

	err = uc.uow.WithTx(ctx, func(tx port.DBTX) error {
		// 2. Se for SQS, registra na inbox para deduplicação no broker
		if input.Source == "SQS" && input.MessageID != "" {
			inboxMsg := inbox.New(input.ConsumerName, input.MessageID, currentHash)
			inserted, err := uc.inboxRepo.CreateIfAbsent(ctx, tx, inboxMsg)
			if err != nil {
				return err
			}
			if !inserted {
				// Reentrega via SQS: verifica se a transação de negócio já existe
				existingTxn, err := uc.txnRepo.GetByIdempotencyKey(ctx, tx, input.IdempotencyKey)
				if err == nil && existingTxn != nil {
					resBal := existingTxn.Money()
					if existingTxn.ResultBalance() != nil {
						resBal = *existingTxn.ResultBalance()
					}
					output = &ProcessWagerOutput{
						TransactionID:   existingTxn.ID(),
						Status:          existingTxn.Status(),
						Balance:         resBal,
						IdempotentReplay: true,
						FailureCode:     existingTxn.FailureCode(),
					}
					return nil
				}
			}
		}

		// 3. Verificação de idempotência persistente no banco
		existingTxn, err := uc.txnRepo.GetByIdempotencyKey(ctx, tx, input.IdempotencyKey)
		if err == nil && existingTxn != nil {
			// Chave já existe: valida se o payload coincide
			if existingTxn.PayloadHash() != nil && *existingTxn.PayloadHash() != currentHash {
				return errs.ErrPayloadConflict
			}

			// Replay idempotente: retorna o resultado original persistido
			resBal := existingTxn.Money()
			if existingTxn.ResultBalance() != nil {
				resBal = *existingTxn.ResultBalance()
			}
			output = &ProcessWagerOutput{
				TransactionID:   existingTxn.ID(),
				Status:          existingTxn.Status(),
				Balance:         resBal,
				IdempotentReplay: true,
				FailureCode:     existingTxn.FailureCode(),
			}
			return nil
		} else if err != nil && !errors.Is(err, errs.ErrTransactionNotFound) {
			return err
		}

		// Verifica se a operação externa já existe com outra chave de idempotência
		existingByExternal, err := uc.txnRepo.GetByProviderAndExternalID(ctx, tx, input.ProviderID, input.ExternalTransactionID)
		if err == nil && existingByExternal != nil {
			return errs.ErrPayloadConflict
		} else if err != nil && !errors.Is(err, errs.ErrTransactionNotFound) {
			return err
		}

		// 4. Lock pessimista na carteira correspondente
		w, err := uc.walletRepo.GetByIDForUpdate(ctx, tx, input.WalletID)
		if err != nil {
			return err
		}

		if w.PlayerID() != input.PlayerID {
			return errs.Newf(errs.CodeInvalidInput, "wallet does not belong to the specified player")
		}
		if w.Currency() != input.Money.Currency() {
			return errs.ErrCurrencyMismatch
		}

		// 5. Inicialização da entidade WagerTransaction
		txnID := uuid.New()
		txn, err := transaction.NewExternal(
			txnID, input.WalletID, input.PlayerID,
			input.ProviderID, input.ExternalTransactionID,
			input.IdempotencyKey, currentHash,
			input.RoundID, input.GameID,
			input.Kind, input.Money,
			input.ReferenceExternalTransactionID,
			&input.CorrelationID,
		)
		if err != nil {
			return err
		}

		// 6. Processamento de acordo com o Kind
		var refTxn *transaction.WagerTransaction
		if input.Kind.RequiresReference() {
			refTxn, err = uc.txnRepo.GetByProviderAndExternalID(ctx, tx, input.ProviderID, *input.ReferenceExternalTransactionID)
			if err != nil && !errors.Is(err, errs.ErrTransactionNotFound) {
				return err
			}

			// Se a referência ainda não existe: PENDING_REFERENCE
			if refTxn == nil {
				retryAfter := time.Now().UTC()
				if err := txn.MarkPendingReference(retryAfter); err != nil {
					return err
				}
				if err := uc.txnRepo.Create(ctx, tx, txn); err != nil {
					return err
				}

				// Emite WagerTransactionPendingReference via Outbox
				evtPending := events.NewWagerTransactionPendingReference(
					txnID, input.WalletID, input.PlayerID,
					input.ProviderID, input.ExternalTransactionID,
					string(input.Kind), *input.ReferenceExternalTransactionID,
					txn.RetryCount(), input.CorrelationID, nil,
				)
				outboxEvt, err := outbox.New(
					evtPending.EventID, evtPending.EventType, txnID, "WagerTransaction",
					&input.CorrelationID, nil, evtPending, time.Now().UTC(), 1,
				)
				if err != nil {
					return err
				}
				if err := uc.outboxRepo.Create(ctx, tx, outboxEvt); err != nil {
					return err
				}

				output = &ProcessWagerOutput{
					TransactionID:   txnID,
					Status:          transaction.StatusPendingReference,
					Balance:         w.Balance(),
					IdempotentReplay: false,
				}
				return nil
			}

			// Se a referência existe, valida se está apta para reversão
			if err := uc.validateReference(refTxn, input, w); err != nil {
				failCode := err.Error()
				var domErr *errs.DomainError
				if errors.As(err, &domErr) {
					failCode = domErr.Code
				}
				if err := uc.rejectTransaction(ctx, tx, txn, w, input, failCode); err != nil {
					return err
				}
				output = &ProcessWagerOutput{
					TransactionID:   txn.ID(),
					Status:          transaction.StatusRejected,
					Balance:         w.Balance(),
					IdempotentReplay: false,
					FailureCode:     &failCode,
				}
				return nil
			}

			txn.ResolveReference(refTxn.ID())
		}

		// Executa mutação financeira na carteira
		var (
			balanceBefore money.Money
			opErr         error
			dirStr        string
		)

		switch input.Kind {
		case transaction.KindBet:
			balanceBefore, opErr = w.Debit(input.Money)
			dirStr = string(ledger.DirectionDebit)

		case transaction.KindWin:
			balanceBefore, opErr = w.Credit(input.Money)
			dirStr = string(ledger.DirectionCredit)

		case transaction.KindLoss:
			// LOSS não altera saldo nem gera lançamento no ledger
			dirStr = ""

		case transaction.KindRefund:
			balanceBefore, opErr = w.Credit(input.Money)
			dirStr = string(ledger.DirectionCredit)

		case transaction.KindRollback:
			refKind := refTxn.Kind()
			dirStr, err = transaction.DirectionFor(transaction.KindRollback, &refKind)
			if err != nil {
				return err
			}
			if dirStr == string(ledger.DirectionDebit) {
				balanceBefore, opErr = w.DebitForRollback(input.Money)
			} else {
				balanceBefore, opErr = w.Credit(input.Money)
			}
		}

		// Rejeição por regra de negócio (ex.: fundos insuficientes)
		if opErr != nil {
			var domErr *errs.DomainError
			failCode := errs.CodeInsufficientFunds
			if errors.As(opErr, &domErr) {
				failCode = domErr.Code
			}
			if err := uc.rejectTransaction(ctx, tx, txn, w, input, failCode); err != nil {
				return err
			}
			output = &ProcessWagerOutput{
				TransactionID:   txn.ID(),
				Status:          transaction.StatusRejected,
				Balance:         w.Balance(),
				IdempotentReplay: false,
				FailureCode:     &failCode,
			}
			return nil
		}

		// Sucesso: transiciona transação para PROCESSED e atualiza carteira
		if err := txn.MarkProcessed(w.Balance()); err != nil {
			return err
		}

		// Cria a transação no banco antes do ledger para satisfazer a foreign key
		if err := uc.txnRepo.Create(ctx, tx, txn); err != nil {
			return err
		}

		if dirStr != "" {
			if err := uc.walletRepo.Update(ctx, tx, w); err != nil {
				return err
			}

			// Gera lançamento imutável no ledger
			ledgerEntry, err := ledger.New(
				input.WalletID,
				txnID,
				ledger.Direction(dirStr),
				input.Money,
				balanceBefore,
				w.Balance(),
			)
			if err != nil {
				return fmt.Errorf("process wager: create ledger: %w", err)
			}
			if err := uc.ledgerRepo.Create(ctx, tx, ledgerEntry); err != nil {
				return err
			}

			// Outbox: WalletBalanceChanged
			balEvt := events.NewWalletBalanceChanged(
				input.WalletID,
				txnID,
				dirStr,
				input.Money,
				balanceBefore,
				w.Balance(),
				w.Version(),
				input.CorrelationID,
				nil,
			)
			outBal, err := outbox.New(
				balEvt.EventID, balEvt.EventType, input.WalletID, "Wallet",
				&input.CorrelationID, nil, balEvt, time.Now().UTC(), 1,
			)
			if err != nil {
				return err
			}
			if err := uc.outboxRepo.Create(ctx, tx, outBal); err != nil {
				return err
			}
		}

		// Outbox: WagerTransactionProcessed (emitido inclusive para LOSS)
		provID := input.ProviderID
		extID := input.ExternalTransactionID
		procEvt := events.NewWagerTransactionProcessed(
			txnID, input.WalletID, input.PlayerID,
			&provID, &extID,
			string(input.Kind),
			input.Money,
			w.Balance(),
			input.CorrelationID,
			nil,
		)
		outProc, err := outbox.New(
			procEvt.EventID, procEvt.EventType, txnID, "WagerTransaction",
			&input.CorrelationID, nil, procEvt, time.Now().UTC(), 1,
		)
		if err != nil {
			return err
		}
		if err := uc.outboxRepo.Create(ctx, tx, outProc); err != nil {
			return err
		}

		output = &ProcessWagerOutput{
			TransactionID:   txnID,
			Status:          transaction.StatusProcessed,
			Balance:         w.Balance(),
			IdempotentReplay: false,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return output, nil
}

func (uc *ProcessWagerUseCase) validateReference(
	refTxn *transaction.WagerTransaction,
	input ProcessWagerInput,
	w *wallet.Wallet,
) error {
	if refTxn.Status() != transaction.StatusProcessed {
		return errs.ErrReferenceNotProcessable
	}
	if refTxn.PlayerID() != input.PlayerID || refTxn.WalletID() != input.WalletID {
		return errs.Newf(errs.CodeInvalidInput, "reference transaction player or wallet mismatch")
	}
	if refTxn.Money().Currency() != input.Money.Currency() {
		return errs.ErrCurrencyMismatch
	}
	if refTxn.RoundID() == nil || *refTxn.RoundID() != input.RoundID {
		return errs.Newf(errs.CodeInvalidInput, "reference roundId mismatch")
	}
	if !refTxn.Money().Equal(input.Money) {
		return errs.Newf(errs.CodeInvalidInput, "reversal amount must match referenced transaction amount")
	}

	if input.Kind == transaction.KindRefund && refTxn.Kind() != transaction.KindBet {
		return errs.Newf(errs.CodeInvalidInput, "REFUND must reference a BET transaction")
	}
	return nil
}

func (uc *ProcessWagerUseCase) rejectTransaction(
	ctx context.Context,
	tx port.DBTX,
	txn *transaction.WagerTransaction,
	w *wallet.Wallet,
	input ProcessWagerInput,
	failureCode string,
) error {
	if err := txn.MarkRejected(failureCode); err != nil {
		return err
	}
	if err := uc.txnRepo.Create(ctx, tx, txn); err != nil {
		return err
	}

	// Outbox: WagerTransactionRejected
	provID := input.ProviderID
	extID := input.ExternalTransactionID
	rejEvt := events.NewWagerTransactionRejected(
		txn.ID(), input.WalletID, input.PlayerID,
		&provID, &extID,
		string(input.Kind),
		failureCode,
		input.CorrelationID,
		nil,
	)
	outRej, err := outbox.New(
		rejEvt.EventID, rejEvt.EventType, txn.ID(), "WagerTransaction",
		&input.CorrelationID, nil, rejEvt, time.Now().UTC(), 1,
	)
	if err != nil {
		return err
	}
	return uc.outboxRepo.Create(ctx, tx, outRej)
}
