package usecase

import (
	"context"
	"fmt"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/felipecristiano/desafio/internal/domain/money"
	"github.com/google/uuid"
)

type ReconcileWalletInput struct {
	WalletID uuid.UUID
}

type ReconcileWalletOutput struct {
	WalletID          uuid.UUID   `json:"walletId"`
	StoredBalance     money.Money `json:"storedBalance"`
	CalculatedBalance money.Money `json:"calculatedBalance"`
	Difference        money.Money `json:"difference"`
	Consistent        bool        `json:"consistent"`
	CheckedEntries    int         `json:"checkedEntries"`
}

type ReconcileWalletUseCase struct {
	uow        port.UnitOfWork
	walletRepo port.WalletRepository
	ledgerRepo port.LedgerRepository
}

func NewReconcileWalletUseCase(
	uow port.UnitOfWork,
	walletRepo port.WalletRepository,
	ledgerRepo port.LedgerRepository,
) *ReconcileWalletUseCase {
	return &ReconcileWalletUseCase{
		uow:        uow,
		walletRepo: walletRepo,
		ledgerRepo: ledgerRepo,
	}
}

func (uc *ReconcileWalletUseCase) Execute(ctx context.Context, input ReconcileWalletInput) (*ReconcileWalletOutput, error) {
	var output *ReconcileWalletOutput

	err := uc.uow.WithTx(ctx, func(tx port.DBTX) error {
		// Leitura consistente do saldo armazenado na carteira
		w, err := uc.walletRepo.GetByID(ctx, tx, input.WalletID)
		if err != nil {
			return err
		}

		// Reconstrução do saldo a partir de todos os créditos e débitos no ledger
		calcBal, count, err := uc.ledgerRepo.CalculateBalance(ctx, tx, input.WalletID)
		if err != nil {
			return err
		}

		// difference = storedBalance - calculatedBalance
		diff, err := w.Balance().Sub(calcBal)
		if err != nil {
			return fmt.Errorf("reconcile: sub balance: %w", err)
		}

		consistent := diff.IsZero()

		output = &ReconcileWalletOutput{
			WalletID:          w.ID(),
			StoredBalance:     w.Balance(),
			CalculatedBalance: calcBal,
			Difference:        diff,
			Consistent:        consistent,
			CheckedEntries:    count,
		}
		return nil
	})

	if err != nil {
		return nil, err
	}
	return output, nil
}
