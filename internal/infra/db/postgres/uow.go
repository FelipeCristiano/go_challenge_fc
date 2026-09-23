package postgres

import (
	"context"
	"fmt"

	"github.com/felipecristiano/desafio/internal/application/port"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type pgxUnitOfWork struct {
	pool *pgxpool.Pool
}

func NewUnitOfWork(pool *pgxpool.Pool) port.UnitOfWork {
	return &pgxUnitOfWork{pool: pool}
}

func (u *pgxUnitOfWork) WithTx(ctx context.Context, fn func(tx port.DBTX) error) error {
	tx, err := u.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel: pgx.ReadCommitted,
	})
	if err != nil {
		return fmt.Errorf("postgres uow: begin tx: %w", err)
	}

	defer func() {
		_ = tx.Rollback(ctx)
	}()

	if err := fn(tx); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres uow: commit tx: %w", err)
	}

	return nil
}
